package agent

import (
	"context"
	"fmt"
	"log"
	"net/http"
)

// loadImages fetches each pushed archive from the control plane, streams it
// into `docker load` and reports the result. The archive is never held in
// memory or written to disk here.
func (a *Agent) loadImages(ctx context.Context, items []loadImage) {
	for _, it := range items {
		errText := ""
		if err := a.loadOneImage(ctx, it); err != nil {
			errText = err.Error()
			log.Printf("load image %s: %v", it.Image, err)
		} else {
			log.Printf("loaded image %s", it.Image)
		}
		body := map[string]string{"id": it.ID, "error": errText}
		if err := a.post(ctx, "/v1/agent/"+a.cfg.NodeID+"/image-loaded", body, nil); err != nil {
			log.Printf("report image load: %v", err)
		}
	}
}

func (a *Agent) loadOneImage(ctx context.Context, it loadImage) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ControlPlaneURL+"/v1/agent/"+a.cfg.NodeID+"/image-load/"+it.ID, nil)
	if err != nil {
		return err
	}
	resp, err := a.doStream(req)
	if err != nil {
		return fmt.Errorf("fetch archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fetch archive: control plane returned %s: %s", resp.Status, errorBody(resp))
	}
	if err := a.docker.LoadImage(ctx, resp.Body); err != nil {
		return err
	}
	if !a.docker.HasImage(ctx, it.Image) {
		return fmt.Errorf("the archive loaded, but this node still has no image named %s (was it saved with that tag?)", it.Image)
	}
	return nil
}
