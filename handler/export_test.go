package handler

import "go.uber.org/zap"

// NewTestHandler builds a Handler with a stub lookup source, letting the
// external handler_test package exercise ServeHTTP without a real *app.App.
// Callers can pass any value implementing the (unexported) geoLookuper seam.
func NewTestHandler(logger *zap.Logger, lookup geoLookuper) *Handler {
	return &Handler{logger: logger, app: lookup}
}
