// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
)

// modelSeesImages says whether pictures should be sent to a model: its server reports the vision
// capability (Ollama does), or an admin listed it in the vision_models setting (OpenAI-compatible
// servers report nothing). Otherwise a picture is read as text with OCR through read_file.
func (a *App) modelSeesImages(ctx context.Context, pr provider.Provider, model string) bool {
	for _, n := range strings.Split(a.setting(ctx, SettingVisionModels, ""), ",") {
		if strings.TrimSpace(n) == model {
			return true
		}
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, err := pr.ModelDetail(c, model)
	return err == nil && d.SupportsVision != nil && *d.SupportsVision
}
