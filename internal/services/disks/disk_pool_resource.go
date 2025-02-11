package disks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonschema"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/location"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/tags"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-azurerm/internal/locks"
	"github.com/hashicorp/terraform-provider-azurerm/internal/sdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/disks/sdk/2021-08-01/diskpools"
	disksValidate "github.com/hashicorp/terraform-provider-azurerm/internal/services/disks/validate"
	networkValidate "github.com/hashicorp/terraform-provider-azurerm/internal/services/network/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
)

var _ sdk.ResourceWithUpdate = DiskPoolResource{}

type DiskPoolResource struct{}

type DiskPoolResourceModel struct {
	Name              string                 `tfschema:"name"`
	ResourceGroupName string                 `tfschema:"resource_group_name"`
	Location          string                 `tfschema:"location"`
	Sku               string                 `tfschema:"sku_name"`
	SubnetId          string                 `tfschema:"subnet_id"`
	Tags              map[string]interface{} `tfschema:"tags"`
	Zones             []string               `tfschema:"zones"`
}

func (DiskPoolResource) Arguments() map[string]*schema.Schema {
	return map[string]*pluginsdk.Schema{
		"name": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: disksValidate.DiskPoolName(),
		},

		"location": commonschema.Location(),

		"resource_group_name": commonschema.ResourceGroupName(),

		"sku_name": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: disksValidate.DiskPoolSku(),
		},

		"subnet_id": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: networkValidate.SubnetID,
		},

		"tags": commonschema.Tags(),

		"zones": { // TODO: create commonschema.ZonesForceNew
			Type:     pluginsdk.TypeList,
			Required: true,
			ForceNew: true,
			MinItems: 1,
			Elem: &pluginsdk.Schema{
				Type:         pluginsdk.TypeString,
				ValidateFunc: validation.StringIsNotEmpty,
			},
		},
	}
}

func (DiskPoolResource) Attributes() map[string]*schema.Schema {
	return map[string]*schema.Schema{}
}

func (DiskPoolResource) ModelObject() interface{} {
	return &DiskPoolResourceModel{}
}

func (DiskPoolResource) ResourceType() string {
	return "azurerm_disk_pool"
}

func (r DiskPoolResource) Create() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 30 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			subscriptionId := metadata.Client.Account.SubscriptionId
			client := metadata.Client.Disks.DiskPoolsClient

			m := DiskPoolResourceModel{}
			err := metadata.Decode(&m)
			if err != nil {
				return err
			}

			id := diskpools.NewDiskPoolID(subscriptionId, m.ResourceGroupName, m.Name)
			existing, err := client.Get(ctx, id)
			if err != nil && !response.WasNotFound(existing.HttpResponse) {
				return fmt.Errorf("checking for presence of existing %q: %+v", id, err)
			}
			if !response.WasNotFound(existing.HttpResponse) {
				return metadata.ResourceRequiresImport(r.ResourceType(), id)
			}

			createParameter := diskpools.DiskPoolCreate{
				Location: location.Normalize(m.Location),
				Properties: diskpools.DiskPoolCreateProperties{
					AvailabilityZones: &m.Zones,
					SubnetId:          m.SubnetId,
				},
				Sku:  expandDisksPoolSku(m.Sku),
				Tags: tags.Expand(m.Tags),
			}
			if err := client.CreateOrUpdateThenPoll(ctx, id, createParameter); err != nil {
				return fmt.Errorf("creating %s: %+v", id, err)
			}
			metadata.SetID(id)
			return nil
		},
	}
}

func (DiskPoolResource) Read() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 5 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			client := metadata.Client.Disks.DiskPoolsClient
			id, err := diskpools.ParseDiskPoolID(metadata.ResourceData.Id())
			if err != nil {
				return err
			}
			resp, err := client.Get(ctx, *id)
			if err != nil {
				if response.WasNotFound(resp.HttpResponse) {
					return metadata.MarkAsGone(id)
				}

				return fmt.Errorf("retrieving %s: %+v", *id, err)
			}
			m := DiskPoolResourceModel{
				Name:              id.DiskPoolName,
				ResourceGroupName: id.ResourceGroupName,
			}
			if model := resp.Model; model != nil {
				if model.Sku != nil {
					m.Sku = model.Sku.Name
				}
				m.Tags = flattenTags(model.Tags)

				m.Location = location.Normalize(model.Location)
				m.SubnetId = model.Properties.SubnetId
				m.Zones = model.Properties.AvailabilityZones
			}

			return metadata.Encode(&m)
		},
	}
}

func (DiskPoolResource) Delete() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 30 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			client := metadata.Client.Disks.DiskPoolsClient
			id, err := diskpools.ParseDiskPoolID(metadata.ResourceData.Id())
			if err != nil {
				return err
			}

			locks.ByID(id.ID())
			defer locks.UnlockByID(id.ID())

			if err := client.DeleteThenPoll(ctx, *id); err != nil {
				return fmt.Errorf("deleting %s: %+v", *id, err)
			}

			return nil
		},
	}
}

func (DiskPoolResource) IDValidationFunc() pluginsdk.SchemaValidateFunc {
	return diskpools.ValidateDiskPoolID
}

func (DiskPoolResource) Update() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 30 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			client := metadata.Client.Disks.DiskPoolsClient
			id, err := diskpools.ParseDiskPoolID(metadata.ResourceData.Id())
			if err != nil {
				return err
			}

			locks.ByID(metadata.ResourceData.Id())
			defer locks.UnlockByID(metadata.ResourceData.Id())

			patch := diskpools.DiskPoolUpdate{}
			var m DiskPoolResourceModel
			if err = metadata.Decode(&m); err != nil {
				return fmt.Errorf("decoding model: %+v", err)
			}

			if metadata.ResourceData.HasChange("sku") {
				sku := expandDisksPoolSku(m.Sku)
				patch.Sku = &sku
			}
			if metadata.ResourceData.HasChange("tags") {
				patch.Tags = tags.Expand(m.Tags)
			}

			if err := client.UpdateThenPoll(ctx, *id, patch); err != nil {
				return fmt.Errorf("updating %s: %+v", *id, err)
			}

			return nil
		},
	}
}

func expandDisksPoolSku(sku string) diskpools.Sku {
	parts := strings.Split(sku, "_")
	return diskpools.Sku{
		Name: sku,
		Tier: &parts[0],
	}
}
