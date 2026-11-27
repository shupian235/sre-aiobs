package source

import (
	"context"
	"errors"
	"net/http"
)

func (c Client) Monitor(ctx context.Context) error {
	switch c.Config.Kind {
	case "prometheus", "victoriametrics":
		var result struct {
			Status string `json:"status"`
		}
		if err := c.request(ctx, http.MethodGet, "/api/v1/query?query=vector%281%29", nil, &result); err != nil {
			return err
		}
		if result.Status != "success" {
			return errors.New("metrics endpoint returned an unsuccessful query")
		}
	case "kubernetes":
		var result struct {
			GitVersion string `json:"gitVersion"`
		}
		if err := c.request(ctx, http.MethodGet, "/version", nil, &result); err != nil {
			return err
		}
		if result.GitVersion == "" {
			return errors.New("Kubernetes endpoint did not return a version")
		}
	default:
		return errors.New("unsupported monitoring source")
	}
	return nil
}
