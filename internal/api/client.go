package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

type Client struct {
	baseURL string
	http    *http.Client
}

type Health struct {
	OK     bool `json:"ok"`
	Routes int  `json:"routes"`
	Logs   int  `json:"logs"`
}

func New(cfg config.Config) *Client {
	return &Client{
		baseURL: "http://" + cfg.API.Listen,
		http: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.do(ctx, http.MethodGet, "/health", nil, &health)
	return health, err
}

func (c *Client) ListRoutes(ctx context.Context) ([]routes.Route, error) {
	var list []routes.Route
	err := c.do(ctx, http.MethodGet, "/routes", nil, &list)
	return list, err
}

func (c *Client) AddRoute(ctx context.Context, route routes.Route) error {
	return c.do(ctx, http.MethodPost, "/routes", route, &route)
}

func (c *Client) DeleteRoute(ctx context.Context, hostname string) error {
	return c.do(ctx, http.MethodDelete, "/routes/"+url.PathEscape(hostname), nil, nil)
}

func (c *Client) ListLogs(ctx context.Context, filter requestlog.Filter) ([]requestlog.Entry, error) {
	values := url.Values{}
	if filter.Hostname != "" {
		values.Set("hostname", filter.Hostname)
	}
	if filter.AfterID > 0 {
		values.Set("after", fmt.Sprint(filter.AfterID))
	}
	if filter.Limit > 0 {
		values.Set("limit", fmt.Sprint(filter.Limit))
	}

	path := "/logs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var entries []requestlog.Entry
	err := c.do(ctx, http.MethodGet, path, nil, &entries)
	return entries, err
}

func (c *Client) GetExchange(ctx context.Context, id uint64) (requestlog.Exchange, error) {
	var exchange requestlog.Exchange
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/logs/%d", id), nil, &exchange)
	return exchange, err
}

func (c *Client) ReplayRequest(ctx context.Context, id uint64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/logs/%d/replay", id), nil, nil)
}

func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/shutdown", nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if strings.TrimSpace(apiErr.Error) == "" {
			apiErr.Error = resp.Status
		}
		return fmt.Errorf("vtunnel API: %s", apiErr.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
