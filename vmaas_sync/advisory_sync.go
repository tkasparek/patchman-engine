package vmaas_sync

import (
	"app/base"
	"app/base/database"
	"app/base/models"
	"app/base/utils"
	"context"
	"github.com/RedHatInsights/patchman-clients/vmaas"
	"github.com/antihax/optional"
	"github.com/pkg/errors"
	"time"
)

var (
	vmaasClient *vmaas.APIClient
)

func configure() {
	cfg := vmaas.NewConfiguration()
	cfg.BasePath = utils.GetenvOrFail("VMAAS_ADDRESS") + base.VMAAS_API_PREFIX
	cfg.Debug = true

	vmaasClient = vmaas.NewAPIClient(cfg)
}

func parseAdvisories(data map[string]vmaas.ErrataResponseErrataList) (models.AdvisoryMetadataSlice, error) {
	var advisories models.AdvisoryMetadataSlice

	// We use advisory types from DB
	var advisoryTypesArr []models.AdvisoryType
	advisoryTypes := map[string]int{}

	err := database.Db.Find(&advisoryTypesArr).Error
	if err != nil {
		return nil, errors.WithMessage(err, "Loading advisory types")
	}

	for _, t := range advisoryTypesArr {
		advisoryTypes[t.Name] = t.ID
	}

	for n, v := range data {
		// Should we skip or report invalid erratas ?
		issued, err := time.Parse(base.RFC_3339_NO_TZ, v.Issued)
		if err != nil {
			utils.Log("err", err.Error()).Error("Invalid errata issued date")
			continue
		}
		modified, err := time.Parse(base.RFC_3339_NO_TZ, v.Updated)
		if err != nil {
			utils.Log("err", err.Error()).Error("Invalid errata modified date")
			continue
		}

		if v.Description == "" || v.Summary == "" {
			utils.Log().Error("An advisory without description or summary")
			continue
		}

		advisory := models.AdvisoryMetadata{
			Name:           n,
			AdvisoryTypeId: advisoryTypes[v.Type],
			Description:    v.Description,
			Synopsis:       v.Synopsis,
			Summary:        v.Summary,
			Solution:       v.Solution,
			PublicDate:     issued,
			ModifiedDate:   modified,
			Url:            &v.Url,
		}

		advisories = append(advisories, advisory)
	}
	return advisories, nil
}

func storeAdvisories(data map[string]vmaas.ErrataResponseErrataList) error {

	advisories, err := parseAdvisories(data)
	if err != nil {
		return errors.WithMessage(err, "Parsing advisories")
	}
	if advisories == nil || len(advisories) == 0 {
		return nil
	}

	tx := database.OnConflictUpdate(database.Db, "name", "description", "synopsis", "summary", "solution", "public_date", "modified_date", "url")
	errors := database.BulkInsertChunk(tx, advisories.ToInterfaceSlice(), 1000)

	if errors != nil && len(errors) > 0 {
		return errors[0]
	}
	return nil

}

func syncAdvisories() error {
	ctx := context.Background()

	if vmaasClient == nil {

		panic("VMaaS client is nil")
	}

	pageIdx := 0
	maxPageIdx := 1

	for pageIdx <= maxPageIdx {

		opts := vmaas.AppErrataHandlerPostPostOpts{
			ErrataRequest: optional.NewInterface(vmaas.ErrataRequest{
				Page:          float32(pageIdx),
				PageSize:      1000,
				ErrataList:    []string{".*"},
				ModifiedSince: "",
			}),
		}

		data, _, err := vmaasClient.ErrataApi.AppErrataHandlerPostPost(ctx, &opts)

		if err != nil {
			return errors.WithMessage(err, "Downloading erratas")
		}

		maxPageIdx = int(data.Pages)
		pageIdx += 1

		utils.Log("count", len(data.ErrataList)).Debug("Downloaded advisories")

		err = storeAdvisories(data.ErrataList)
		if err != nil {
			return errors.WithMessage(err, "Storing advisories")

		}
	}
	return nil
}