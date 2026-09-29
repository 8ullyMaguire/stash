package scraper

import (
	"context"
	"net/http"
	"strings"

	"github.com/stashapp/stash/pkg/models"
)

func setPerformerImage(ctx context.Context, client *http.Client, p *models.ScrapedPerformer, globalConfig GlobalConfig) error {
	// backwards compatibility: we fetch the image if it's a URL and set it to the first image
	// Image is deprecated, so only do this if Images is unset
	if p.Image == nil || len(p.Images) > 0 {
		// nothing to do
		return nil
	}

	// don't try to get the image if it doesn't appear to be a URL
	if !strings.HasPrefix(*p.Image, "http") {
		p.Images = []string{*p.Image}
		return nil
	}

	img, err := getImage(ctx, *p.Image, client, globalConfig)
	if err != nil {
		return err
	}

	p.Image = img
	// Image is deprecated. Use images instead
	p.Images = []string{*img}

	return nil
}

func setStudioImage(ctx context.Context, client *http.Client, p *models.ScrapedStudio, globalConfig GlobalConfig) error {
	// backwards compatibility: we fetch the image if it's a URL and set it to the first image
	// Image is deprecated, so only do this if Images is unset
	if p.Image == nil || len(p.Images) > 0 {
		// nothing to do
		return nil
	}

	// don't try to get the image if it doesn't appear to be a URL
	if !strings.HasPrefix(*p.Image, "http") {
		p.Images = []string{*p.Image}
		return nil
	}

	img, err := getImage(ctx, *p.Image, client, globalConfig)
	if err != nil {
		return err
	}

	p.Image = img
	// Image is deprecated. Use images instead
	p.Images = []string{*img}

	return nil
}

func processImageField(ctx context.Context, imageField *string, client *http.Client, globalConfig GlobalConfig) error {
	if imageField == nil {
		return nil
	}

	// don't try to get the image if it doesn't appear to be a URL
	// this allows scrapers to return base64 data URIs directly
	if !strings.HasPrefix(*imageField, "http") {
		return nil
	}

	img, err := getImage(ctx, *imageField, client, globalConfig)
	if err != nil {
		return err
	}

	*imageField = *img
	return nil
}

type imageGetter struct {
	client          *http.Client
	globalConfig    GlobalConfig
	requestModifier func(req *http.Request)
}

func getImage(ctx context.Context, url string, client *http.Client, globalConfig GlobalConfig) (*string, error) {
	g := imageGetter{
		client:       client,
		globalConfig: globalConfig,
	}

	return g.getImage(ctx, url)
}

func getStashPerformerImage(ctx context.Context, stashURL string, performerID string, imageGetter imageGetter) (*string, error) {
	return imageGetter.getImage(ctx, stashURL+"/performer/"+performerID+"/image")
}

func getStashSceneImage(ctx context.Context, stashURL string, sceneID string, imageGetter imageGetter) (*string, error) {
	return imageGetter.getImage(ctx, stashURL+"/scene/"+sceneID+"/screenshot")
}
