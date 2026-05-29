package cloudflare

import (
	"context"
	"fmt"
)

const accountAuthorizationHint = "token needs account-level Account Settings Read or a broader account read permission"

type Discovery struct {
	Token       TokenStatus
	Accounts    []Account
	Zones       []Zone
	WildcardDNS map[string][]DNSRecord
	Errors      []string
}

func Discover(ctx context.Context, client *Client) Discovery {
	discovery := Discovery{}

	token, err := client.VerifyToken(ctx)
	if err != nil {
		discovery.Errors = append(discovery.Errors, "token verify: "+err.Error())
		return discovery
	}
	discovery.Token = token

	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		discovery.Errors = append(discovery.Errors, formatAccountWarning(err))
	} else {
		discovery.Accounts = accounts
	}

	zones, err := client.ListZones(ctx)
	if err != nil {
		discovery.Errors = append(discovery.Errors, "zones: "+err.Error())
	} else {
		discovery.Zones = zones
	}
	discovery.Accounts = mergeAccounts(discovery.Accounts, accountsFromZones(discovery.Zones))

	discovery.WildcardDNS = map[string][]DNSRecord{}
	for _, zone := range discovery.Zones {
		records, err := client.ListDNSRecords(ctx, zone.ID, DNSRecordFilter{Name: "*." + zone.Name})
		if err != nil {
			discovery.Errors = append(discovery.Errors, formatDNSWarning(zone, err))
			continue
		}
		discovery.WildcardDNS[zone.ID] = records
	}

	return discovery
}

func formatAccountWarning(err error) string {
	warning := "accounts: " + err.Error()
	if IsAuthorizationError(err) {
		warning += " (" + accountAuthorizationHint + ")"
	}
	return warning
}

func mergeAccounts(primary []Account, fallback []Account) []Account {
	seen := map[string]bool{}
	merged := make([]Account, 0, len(primary)+len(fallback))
	for _, account := range append(primary, fallback...) {
		if account.ID == "" || seen[account.ID] {
			continue
		}
		seen[account.ID] = true
		merged = append(merged, account)
	}
	return merged
}

func accountsFromZones(zones []Zone) []Account {
	accounts := make([]Account, 0, len(zones))
	for _, zone := range zones {
		if zone.Account.ID == "" {
			continue
		}
		accounts = append(accounts, Account{ID: zone.Account.ID, Name: zone.Account.Name})
	}
	return accounts
}

func formatDNSWarning(zone Zone, err error) string {
	label := zone.Name
	if label == "" {
		label = zone.ID
	}
	warning := fmt.Sprintf("dns records for %s: %s", label, err.Error())
	if IsAuthorizationError(err) {
		warning += " (token needs zone-level DNS Read access)"
	}
	return warning
}
