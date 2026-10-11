package indexer

import (
	"context"
	"os"

	"github.com/rs/zerolog/log"

	"github.com/asciimoo/hister/server/visual"
)

func (i *Indexer) ReadVisual(id string) (*visual.Manifest, error) {
	if i.visual == nil {
		return nil, os.ErrNotExist
	}
	return i.visual.Read(id)
}

func (i *Indexer) ReadVisualHTML(key string) ([]byte, error) {
	if i.visual == nil {
		return nil, os.ErrNotExist
	}
	return i.visual.ReadHTML(key)
}

func (i *Indexer) ReadVisualResource(key, kind string) ([]byte, error) {
	if i.visual == nil {
		return nil, os.ErrNotExist
	}
	return i.visual.ReadResource(key, kind)
}

func (i *Indexer) storeVisual(id string, plan documentWritePlan) {
	if i.visual == nil || plan.visualCapture == nil || i.disablePreviews || i.visualConfig == nil || i.visualConfig.VisualMode() == "disabled" {
		return
	}
	ctx := plan.visualContext
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = visual.WithCodecs(ctx, i.visualConfig.VisualArchive.AVIFEncoder, i.visualConfig.VisualArchive.AVIFDecoder)
	if err := i.visual.SaveIfPresent(ctx, id, plan.visualCapture, func(id string) bool {
		return i.visualConfig.VisualMode() != "disabled" && i.getStoredDocumentState(id).found
	}); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("visual capture failed; searchable document retained")
	}
}

func (i *Indexer) deleteVisual(id string) {
	if i.visual == nil {
		return
	}
	if err := i.visual.Delete(id); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("failed to delete visual archive")
	}
}
