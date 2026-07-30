package adobe

import "context"

type Summary struct {
	Enabled          bool `json:"enabled"`
	AccountsTotal    int  `json:"accounts_total"`
	AccountsReady    int  `json:"accounts_ready"`
	AccountsDisabled int  `json:"accounts_disabled"`
	RoutesTotal      int  `json:"routes_total"`
	RoutesHealthy    int  `json:"routes_healthy"`
	RoutesUnhealthy  int  `json:"routes_unhealthy"`
}

func (r *Runtime) Summary(ctx context.Context) (Summary, error) {
	if r == nil || r.repository == nil {
		return Summary{}, nil
	}
	result := Summary{Enabled: true}
	err := r.repository.db.QueryRow(ctx, `
SELECT
 (SELECT COUNT(*) FROM adobe_accounts),
 (SELECT COUNT(*) FROM adobe_accounts WHERE state='ready' AND disabled=FALSE),
 (SELECT COUNT(*) FROM adobe_accounts WHERE disabled=TRUE),
 (SELECT COUNT(*) FROM adobe_routes),
 (SELECT COUNT(*) FROM adobe_routes WHERE enabled=TRUE AND health_status='healthy'),
 (SELECT COUNT(*) FROM adobe_routes WHERE health_status='unhealthy')`).Scan(
		&result.AccountsTotal, &result.AccountsReady, &result.AccountsDisabled,
		&result.RoutesTotal, &result.RoutesHealthy, &result.RoutesUnhealthy,
	)
	return result, err
}
