// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package hdinsight

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/go-azure-helpers/lang/pointer"
	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonschema"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/identity"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/location"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/tags"
	"github.com/hashicorp/go-azure-sdk/resource-manager/hdinsight/2021-06-01/clusters"
	"github.com/hashicorp/go-azure-sdk/resource-manager/hdinsight/2021-06-01/configurations"
	"github.com/hashicorp/go-azure-sdk/resource-manager/hdinsight/2021-06-01/extensions"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/azure"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/tf"
	"github.com/hashicorp/terraform-provider-azurerm/internal/clients"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/timeouts"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
)

// NOTE: this isn't a recommended way of building resources in Terraform
// this pattern is used to work around a generic but pedantic API endpoint
var hdInsightSparkClusterHeadNodeDefinition = HDInsightNodeDefinition{
	CanSpecifyInstanceCount:  false,
	MinInstanceCount:         2,
	MaxInstanceCount:         utils.Int(2),
	CanSpecifyDisks:          false,
	FixedTargetInstanceCount: pointer.To(int64(2)),
}

var hdInsightSparkClusterWorkerNodeDefinition = HDInsightNodeDefinition{
	CanSpecifyInstanceCount: true,
	MinInstanceCount:        1,
	CanSpecifyDisks:         false,
	CanAutoScaleByCapacity:  true,
	CanAutoScaleOnSchedule:  true,
}

var hdInsightSparkClusterZookeeperNodeDefinition = HDInsightNodeDefinition{
	CanSpecifyInstanceCount:  false,
	MinInstanceCount:         3,
	MaxInstanceCount:         utils.Int(3),
	FixedTargetInstanceCount: pointer.To(int64(3)),
	CanSpecifyDisks:          false,
}

func resourceHDInsightSparkCluster() *pluginsdk.Resource {
	return &pluginsdk.Resource{
		Create: resourceHDInsightSparkClusterCreate,
		Read:   resourceHDInsightSparkClusterRead,
		Update: hdinsightClusterUpdate("Spark", resourceHDInsightSparkClusterRead),
		Delete: hdinsightClusterDelete("Spark"),

		Importer: pluginsdk.ImporterValidatingResourceId(func(id string) error {
			_, err := clusters.ParseClusterID(id)
			return err
		}),

		Timeouts: &pluginsdk.ResourceTimeout{
			Create: pluginsdk.DefaultTimeout(60 * time.Minute),
			Read:   pluginsdk.DefaultTimeout(5 * time.Minute),
			Update: pluginsdk.DefaultTimeout(60 * time.Minute),
			Delete: pluginsdk.DefaultTimeout(60 * time.Minute),
		},

		Schema: map[string]*pluginsdk.Schema{
			"name": SchemaHDInsightName(),

			"resource_group_name": commonschema.ResourceGroupName(),

			"location": commonschema.Location(),

			"cluster_version": SchemaHDInsightClusterVersion(),

			"tier": SchemaHDInsightTier(),

			"tls_min_version": SchemaHDInsightTls(),

			"encryption_in_transit_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},

			"disk_encryption": SchemaHDInsightsDiskEncryptionProperties(),

			"component_version": {
				Type:     pluginsdk.TypeList,
				Required: true,
				MaxItems: 1,
				Elem: &pluginsdk.Resource{
					Schema: map[string]*pluginsdk.Schema{
						"spark": {
							Type:     pluginsdk.TypeString,
							Required: true,
							ForceNew: true,
						},
					},
				},
			},

			"compute_isolation": SchemaHDInsightsComputeIsolation(),

			"gateway": SchemaHDInsightsGateway(),

			"metastores": SchemaHDInsightsExternalMetastores(),

			"network": SchemaHDInsightsNetwork(),

			"security_profile": SchemaHDInsightsSecurityProfile(),

			"storage_account": SchemaHDInsightsStorageAccounts(),

			"storage_account_gen2": SchemaHDInsightsGen2StorageAccounts(),

			"roles": {
				Type:     pluginsdk.TypeList,
				Required: true,
				MaxItems: 1,
				Elem: &pluginsdk.Resource{
					Schema: map[string]*pluginsdk.Schema{
						"head_node": SchemaHDInsightNodeDefinition("roles.0.head_node", hdInsightSparkClusterHeadNodeDefinition, true),

						"worker_node": SchemaHDInsightNodeDefinition("roles.0.worker_node", hdInsightSparkClusterWorkerNodeDefinition, true),

						"zookeeper_node": SchemaHDInsightNodeDefinition("roles.0.zookeeper_node", hdInsightSparkClusterZookeeperNodeDefinition, true),
					},
				},
			},

			"https_endpoint": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},

			"ssh_endpoint": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},

			"monitor": SchemaHDInsightsMonitor(),

			"extension": SchemaHDInsightsExtension(),

			"tags": commonschema.Tags(),
		},
	}
}

func resourceHDInsightSparkClusterCreate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).HDInsight.ClustersClient
	subscriptionId := meta.(*clients.Client).Account.SubscriptionId
	extensionsClient := meta.(*clients.Client).HDInsight.ExtensionsClient
	ctx, cancel := timeouts.ForCreate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id := clusters.NewClusterID(subscriptionId, d.Get("resource_group_name").(string), d.Get("name").(string))

	existing, err := client.Get(ctx, id)
	if err != nil {
		if !response.WasNotFound(existing.HttpResponse) {
			return fmt.Errorf("checking for presence of existing %s: %+v", id, err)
		}
	}

	if !response.WasNotFound(existing.HttpResponse) {
		return tf.ImportAsExistsError("azurerm_hdinsight_spark_cluster", id.ID())
	}

	gatewayRaw := d.Get("gateway").([]interface{})
	configurations := expandHDInsightsConfigurations(gatewayRaw)

	metastoresRaw := d.Get("metastores").([]interface{})
	metastores := expandHDInsightsMetastore(metastoresRaw)
	for k, v := range metastores {
		configurations[k] = v
	}

	storageAccountsRaw := d.Get("storage_account").([]interface{})
	storageAccountsGen2Raw := d.Get("storage_account_gen2").([]interface{})
	storageAccounts, i, err := expandHDInsightsStorageAccounts(storageAccountsRaw, storageAccountsGen2Raw)
	if err != nil {
		return fmt.Errorf("expanding `storage_account`: %s", err)
	}

	sparkRoles := hdInsightRoleDefinition{
		HeadNodeDef:      hdInsightSparkClusterHeadNodeDefinition,
		WorkerNodeDef:    hdInsightSparkClusterWorkerNodeDefinition,
		ZookeeperNodeDef: hdInsightSparkClusterZookeeperNodeDefinition,
	}
	rolesRaw := d.Get("roles").([]interface{})
	roles, err := expandHDInsightRoles(rolesRaw, sparkRoles)
	if err != nil {
		return fmt.Errorf("expanding `roles`: %+v", err)
	}

	encryptionInTransit := d.Get("encryption_in_transit_enabled").(bool)

	params := clusters.ClusterCreateParametersExtended{
		Location: pointer.To(azure.NormalizeLocation(d.Get("location").(string))),
		Properties: &clusters.ClusterCreateProperties{
			Tier:           pointer.To(clusters.Tier(d.Get("tier").(string))),
			OsType:         pointer.To(clusters.OSTypeLinux),
			ClusterVersion: pointer.To(d.Get("cluster_version").(string)),
			EncryptionInTransitProperties: &clusters.EncryptionInTransitProperties{
				IsEncryptionInTransitEnabled: &encryptionInTransit,
			},
			MinSupportedTlsVersion: pointer.To(d.Get("tls_min_version").(string)),
			NetworkProperties:      expandHDInsightsNetwork(d.Get("network").([]interface{})),
			ClusterDefinition: &clusters.ClusterDefinition{
				Kind:             pointer.To("Spark"),
				ComponentVersion: expandHDInsightSparkComponentVersion(d.Get("component_version").([]interface{})),
				Configurations:   pointer.To(interface{}(configurations)),
			},
			StorageProfile: &clusters.StorageProfile{
				Storageaccounts: storageAccounts,
			},
			ComputeProfile: &clusters.ComputeProfile{
				Roles: roles,
			},
			ComputeIsolationProperties: expandHDInsightComputeIsolationProperties(d.Get("compute_isolation").([]interface{})),
		},
		Tags:     tags.Expand(d.Get("tags").(map[string]interface{})),
		Identity: i,
	}

	if diskEncryptionPropertiesRaw, ok := d.GetOk("disk_encryption"); ok {
		params.Properties.DiskEncryptionProperties, err = expandHDInsightsDiskEncryptionProperties(diskEncryptionPropertiesRaw.([]interface{}))
		if err != nil {
			return err
		}
	}

	if v, ok := d.GetOk("security_profile"); ok {
		params.Properties.SecurityProfile = expandHDInsightSecurityProfile(v.([]interface{}))

		params.Identity = &identity.SystemAndUserAssignedMap{
			Type:        identity.TypeUserAssigned,
			IdentityIds: make(map[string]identity.UserAssignedIdentityDetails),
		}

		if params.Properties.SecurityProfile != nil && params.Properties.SecurityProfile.MsiResourceId != nil {
			params.Identity.IdentityIds[*params.Properties.SecurityProfile.MsiResourceId] = identity.UserAssignedIdentityDetails{}
		}
	}

	if err := client.CreateThenPoll(ctx, id, params); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
	}

	d.SetId(id.ID())

	// We can only enable monitoring after creation
	if v, ok := d.GetOk("monitor"); ok {
		monitorRaw := v.([]interface{})
		if err := enableHDInsightMonitoring(ctx, extensionsClient, id, monitorRaw); err != nil {
			return err
		}
	}

	if v, ok := d.GetOk("extension"); ok {
		extensionRaw := v.([]interface{})
		if err := enableHDInsightAzureMonitor(ctx, extensionsClient, id, extensionRaw); err != nil {
			return err
		}
	}

	return resourceHDInsightSparkClusterRead(d, meta)
}

func resourceHDInsightSparkClusterRead(d *pluginsdk.ResourceData, meta interface{}) error {
	clustersClient := meta.(*clients.Client).HDInsight.ClustersClient
	configurationsClient := meta.(*clients.Client).HDInsight.ConfigurationsClient
	extensionsClient := meta.(*clients.Client).HDInsight.ExtensionsClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := clusters.ParseClusterID(d.Id())
	if err != nil {
		return err
	}

	resp, err := clustersClient.Get(ctx, *id)
	if err != nil {
		if response.WasNotFound(resp.HttpResponse) {
			log.Printf("[DEBUG] %s was not found - removing from state!", *id)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	// Each call to configurationsClient methods is HTTP request. Getting all settings in one operation
	configId := configurations.NewClusterID(id.SubscriptionId, id.ResourceGroupName, id.ClusterName)
	configurations, err := configurationsClient.List(ctx, configId)
	if err != nil {
		return fmt.Errorf("retrieving Configuration for %s: %+v", id, err)
	}
	if model := configurations.Model; model != nil {
		if config := configurations.Model.Configurations; config != nil {
			flattenAndSetHDInsightsMetastores(d, *config)

			gateway, exists := (*config)["gateway"]
			if !exists {
				return fmt.Errorf("retrieving gateway for %s: %+v", id, err)
			}

			if err := d.Set("gateway", flattenHDInsightsConfigurations(gateway, d)); err != nil {
				return fmt.Errorf("flattening `gateway`: %+v", err)
			}

		}
	}

	d.Set("name", id.ClusterName)
	d.Set("resource_group_name", id.ResourceGroupName)
	if model := resp.Model; model != nil {
		d.Set("location", location.Normalize(model.Location))

		// storage_account isn't returned so I guess we just leave it ¯\_(ツ)_/¯
		if props := model.Properties; props != nil {
			tier := ""
			// the Azure API is inconsistent here, so rewrite this into the casing we expect
			// todo this should be fixed by the new transport layer?
			for _, v := range clusters.PossibleValuesForTier() {
				if strings.EqualFold(v, string(pointer.From(props.Tier))) {
					tier = v
				}
			}
			d.Set("tier", tier)
			d.Set("cluster_version", props.ClusterVersion)
			d.Set("tls_min_version", props.MinSupportedTlsVersion)

			def := props.ClusterDefinition
			if err := d.Set("component_version", flattenHDInsightSparkComponentVersion(def.ComponentVersion)); err != nil {
				return fmt.Errorf("flattening `component_version`: %+v", err)
			}

			sparkRoles := hdInsightRoleDefinition{
				HeadNodeDef:      hdInsightSparkClusterHeadNodeDefinition,
				WorkerNodeDef:    hdInsightSparkClusterWorkerNodeDefinition,
				ZookeeperNodeDef: hdInsightSparkClusterZookeeperNodeDefinition,
			}

			if props.EncryptionInTransitProperties != nil {
				d.Set("encryption_in_transit_enabled", props.EncryptionInTransitProperties.IsEncryptionInTransitEnabled)
			}

			if props.DiskEncryptionProperties != nil {
				diskEncryptionProps, err := flattenHDInsightsDiskEncryptionProperties(*props.DiskEncryptionProperties)
				if err != nil {
					return err
				}
				if err := d.Set("disk_encryption", diskEncryptionProps); err != nil {
					return fmt.Errorf("flattening setting `disk_encryption`: %+v", err)
				}
			}

			if props.NetworkProperties != nil {
				if err := d.Set("network", FlattenHDInsightsNetwork(props.NetworkProperties)); err != nil {
					return fmt.Errorf("flattening `network`: %+v", err)
				}
			}

			flattenedRoles := flattenHDInsightRoles(d, props.ComputeProfile, sparkRoles)
			if err := d.Set("roles", flattenedRoles); err != nil {
				return fmt.Errorf("flattening `roles`: %+v", err)
			}

			if props.ComputeIsolationProperties != nil {
				if err := d.Set("compute_isolation", FlattenHDInsightComputeIsolationProperties(*props.ComputeIsolationProperties)); err != nil {
					return fmt.Errorf("failed setting `compute_isolation`: %+v", err)
				}
			}

			httpEndpoint := FindHDInsightConnectivityEndpoint("HTTPS", props.ConnectivityEndpoints)
			d.Set("https_endpoint", httpEndpoint)
			sshEndpoint := FindHDInsightConnectivityEndpoint("SSH", props.ConnectivityEndpoints)
			d.Set("ssh_endpoint", sshEndpoint)

			monId := extensions.NewClusterID(id.SubscriptionId, id.ResourceGroupName, id.ClusterName)
			monitor, err := extensionsClient.GetMonitoringStatus(ctx, monId)
			if err != nil {
				return fmt.Errorf("reading monitor configuration for %s: %+v", *id, err)
			}

			d.Set("monitor", flattenHDInsightMonitoring(monitor.Model))

			extension, err := extensionsClient.GetAzureMonitorStatus(ctx, monId)
			if err != nil {
				return fmt.Errorf("reading extension configuration for %s: %+v", *id, err)
			}

			d.Set("extension", flattenHDInsightAzureMonitor(extension.Model))

			if err := d.Set("security_profile", flattenHDInsightSecurityProfile(props.SecurityProfile, d)); err != nil {
				return fmt.Errorf("setting `security_profile`: %+v", err)
			}
		}

		if err = tags.FlattenAndSet(d, model.Tags); err != nil {
			return fmt.Errorf("setting tags: %+v", err)
		}
	}

	return nil
}

func expandHDInsightSparkComponentVersion(input []interface{}) *map[string]string {
	vs := input[0].(map[string]interface{})
	return pointer.To(map[string]string{
		"Spark": vs["spark"].(string),
	})
}

func flattenHDInsightSparkComponentVersion(input *map[string]string) []interface{} {
	sparkVersion := ""
	if input != nil {
		if v, ok := (*input)["Spark"]; ok {
			sparkVersion = v
		}
	}

	return []interface{}{
		map[string]interface{}{
			"spark": sparkVersion,
		},
	}
}
