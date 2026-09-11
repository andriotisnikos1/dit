package cli

import (
	"github.com/andriotisnikos1/dit/internal/apiclient"
)

// watchQueryFromFlags builds a WatchQuery from the `dit watch list` flags.
func watchQueryFromFlags(enabled, disabled bool, registry, search string, limit int) apiclient.WatchQuery {
	q := apiclient.WatchQuery{
		Registry: registry,
		Search:   search,
		Limit:    limit,
	}
	switch {
	case enabled:
		value := true
		q.Enabled = &value
	case disabled:
		value := false
		q.Enabled = &value
	}
	return q
}

// credentialsRegistry extracts the registry host from a 428 error.
func credentialsRegistry(err error) string {
	return apiclient.CredentialsRequiredRegistry(err)
}
