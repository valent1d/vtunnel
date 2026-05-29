package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.cloudflare.com/client/v4"
	BaseURLEnv     = "VTUNNEL_CLOUDFLARE_API_BASE"
)

var ErrMissingToken = errors.New("missing CLOUDFLARE_API_TOKEN")

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type Option func(*Client)

func WithBaseURL(baseURL string) Option {
	return func(client *Client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			client.baseURL = baseURL
		}
	}
}

func New(token string, options ...Option) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrMissingToken
	}
	client := &Client{
		baseURL: DefaultBaseURL,
		token:   token,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

type TokenStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresOn string `json:"expires_on"`
	NotBefore string `json:"not_before"`
}

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Zone struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Status  string     `json:"status"`
	Type    string     `json:"type"`
	Account AccountRef `json:"account"`
}

type AccountRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

type DNSRecordInput struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl,omitempty"`
}

type DNSRecordFilter struct {
	Name string
	Type string
}

func (c *Client) VerifyToken(ctx context.Context) (TokenStatus, error) {
	var token TokenStatus
	err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &token)
	return token, err
}

func (c *Client) VerifyAccountToken(ctx context.Context, accountID string) (TokenStatus, error) {
	var token TokenStatus
	path := "/accounts/" + url.PathEscape(strings.TrimSpace(accountID)) + "/tokens/verify"
	err := c.do(ctx, http.MethodGet, path, nil, &token)
	return token, err
}

func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	err := c.listAll(ctx, "/accounts", func(data json.RawMessage) error {
		var page []Account
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		accounts = append(accounts, page...)
		return nil
	})
	return accounts, err
}

func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	err := c.listAll(ctx, "/zones", func(data json.RawMessage) error {
		var page []Zone
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		zones = append(zones, page...)
		return nil
	})
	return zones, err
}

func (c *Client) ListDNSRecords(ctx context.Context, zoneID string, filter DNSRecordFilter) ([]DNSRecord, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return nil, errors.New("zone id is required")
	}

	values := url.Values{}
	if filter.Name != "" {
		values.Set("name", strings.TrimSpace(filter.Name))
	}
	if filter.Type != "" {
		values.Set("type", strings.TrimSpace(filter.Type))
	}

	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var records []DNSRecord
	err := c.listAll(ctx, path, func(data json.RawMessage) error {
		var page []DNSRecord
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		records = append(records, page...)
		return nil
	})
	return records, err
}

func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, input DNSRecordInput) (DNSRecord, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return DNSRecord{}, errors.New("zone id is required")
	}
	var record DNSRecord
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	err := c.do(ctx, http.MethodPost, path, input, &record)
	return record, err
}

func (c *Client) UpdateDNSRecord(ctx context.Context, zoneID string, recordID string, input DNSRecordInput) (DNSRecord, error) {
	zoneID = strings.TrimSpace(zoneID)
	recordID = strings.TrimSpace(recordID)
	if zoneID == "" {
		return DNSRecord{}, errors.New("zone id is required")
	}
	if recordID == "" {
		return DNSRecord{}, errors.New("dns record id is required")
	}
	var record DNSRecord
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	err := c.do(ctx, http.MethodPut, path, input, &record)
	return record, err
}

func IsAuthorizationError(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden {
		return true
	}
	for _, message := range apiErr.Errors {
		lower := strings.ToLower(message.Message)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "permission") || strings.Contains(lower, "access") {
			return true
		}
	}
	return false
}

func (c *Client) listAll(ctx context.Context, path string, decode func(json.RawMessage) error) error {
	page := 1
	for {
		pagePath := withPagination(path, page, 50)
		envelope, err := c.request(ctx, http.MethodGet, pagePath, nil)
		if err != nil {
			return err
		}
		if err := decode(envelope.Result); err != nil {
			return err
		}
		if envelope.ResultInfo.TotalPages <= page || envelope.ResultInfo.TotalPages == 0 {
			return nil
		}
		page++
	}
}

func (c *Client) do(ctx context.Context, method string, path string, body any, out any) error {
	envelope, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

func (c *Client) request(ctx context.Context, method string, path string, body any) (envelope, error) {
	var reqBody *strings.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return envelope{}, fmt.Errorf("encode Cloudflare request: %w", err)
		}
		reqBody = strings.NewReader(string(data))
	} else {
		reqBody = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return envelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return envelope{}, err
	}
	defer resp.Body.Close()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return envelope{}, fmt.Errorf("decode Cloudflare response: %w", err)
	}
	if resp.StatusCode >= 400 || !env.Success {
		return env, apiError{Status: resp.StatusCode, Errors: env.Errors}
	}
	return env, nil
}

type envelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	Errors     []apiMessage    `json:"errors"`
	Messages   []apiMessage    `json:"messages"`
	ResultInfo resultInfo      `json:"result_info"`
}

type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type apiError struct {
	Status int
	Errors []apiMessage
}

func (e apiError) Error() string {
	messages := make([]string, 0, len(e.Errors))
	for _, apiErr := range e.Errors {
		if apiErr.Message != "" {
			messages = append(messages, apiErr.Message)
		}
	}
	if len(messages) == 0 {
		return fmt.Sprintf("Cloudflare API error: HTTP %d", e.Status)
	}
	return fmt.Sprintf("Cloudflare API error: HTTP %d: %s", e.Status, strings.Join(messages, "; "))
}

func withPagination(path string, page int, perPage int) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(perPage)
}
