package matcher

import "go.uber.org/zap"

// NewTestMatcher builds a Matcher with a stub lookup source, letting the
// external matcher_test package exercise MatchWithError without a real *app.App.
// Callers can pass any value implementing the (unexported) geoLookuper seam.
func NewTestMatcher(logger *zap.Logger, conditions map[string][]string, lookup geoLookuper) *Matcher {
	return &Matcher{Conditions: conditions, app: lookup, logger: logger}
}
