package dynamic

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	mgmtnetwork "github.com/Azure/azure-sdk-for-go/services/network/mgmt/2019-07-01/network"
	mgmtauthorization "github.com/Azure/azure-sdk-for-go/services/preview/authorization/mgmt/2018-09-01-preview/authorization"
	mgmtfeatures "github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-07-01/features"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/mock/gomock"
	"github.com/sirupsen/logrus"

	"github.com/Azure/ARO-RP/pkg/api"
	mock_authorization "github.com/Azure/ARO-RP/pkg/util/mocks/azureclient/mgmt/authorization"
	mock_features "github.com/Azure/ARO-RP/pkg/util/mocks/azureclient/mgmt/features"
	mock_network "github.com/Azure/ARO-RP/pkg/util/mocks/azureclient/mgmt/network"
)

func TestValidateVnetPermissions(t *testing.T) {
	ctx := context.Background()

	resourceGroupName := "testGroup"
	vnetName := "testVnet"
	subscriptionID := "0000000-0000-0000-0000-000000000000"
	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Network/virtualNetworks/" + vnetName
	resourceType := "virtualNetworks"
	resourceProvider := "Microsoft.Network"

	controller := gomock.NewController(t)
	defer controller.Finish()

	for _, tt := range []struct {
		name    string
		mocks   func(*mock_authorization.MockPermissionsClient, func())
		wantErr string
	}{
		{
			name: "pass",
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, resourceProvider, "", resourceType, vnetName).
					Return([]mgmtauthorization.Permission{
						{
							Actions: &[]string{
								"Microsoft.Network/virtualNetworks/join/action",
								"Microsoft.Network/virtualNetworks/read",
								"Microsoft.Network/virtualNetworks/write",
								"Microsoft.Network/virtualNetworks/subnets/join/action",
								"Microsoft.Network/virtualNetworks/subnets/read",
								"Microsoft.Network/virtualNetworks/subnets/write",
							},
							NotActions: &[]string{},
						},
					}, nil)
			},
		},
		{
			name: "fail: missing permissions",
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, resourceProvider, "", resourceType, vnetName).
					Do(func(arg0, arg1, arg2, arg3, arg4, arg5 interface{}) {
						cancel()
					}).
					Return(
						[]mgmtauthorization.Permission{
							{
								Actions:    &[]string{},
								NotActions: &[]string{},
							},
						},
						nil,
					)
			},
			wantErr: "400: InvalidResourceProviderPermissions: : The resource provider does not have Network Contributor permission on vnet '" + vnetID + "'.",
		},
		{
			name: "fail: not found",
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, resourceProvider, "", resourceType, vnetName).
					Do(func(arg0, arg1, arg2, arg3, arg4, arg5 interface{}) {
						cancel()
					}).
					Return(
						nil,
						autorest.DetailedError{
							StatusCode: http.StatusNotFound,
						},
					)
			},
			wantErr: "400: InvalidLinkedVNet: : The vnet '" + vnetID + "' could not be found.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			permissionsClient := mock_authorization.NewMockPermissionsClient(controller)
			tt.mocks(permissionsClient, cancel)

			dv := &dynamic{
				log:         logrus.NewEntry(logrus.StandardLogger()),
				permissions: permissionsClient,
				vnetr: &azure.Resource{
					ResourceGroup:  resourceGroupName,
					ResourceType:   resourceType,
					Provider:       resourceProvider,
					ResourceName:   vnetName,
					SubscriptionID: subscriptionID,
				},
				code: "InvalidResourceProviderPermissions",
				typ:  "resource provider",
			}

			err := dv.ValidateVnetPermissions(ctx)
			if err != nil && err.Error() != tt.wantErr ||
				err == nil && tt.wantErr != "" {
				t.Error(err)
			}
		})
	}
}

func TestGetRouteTableID(t *testing.T) {
	resourceGroupID := "/subscriptions/0000000-0000-0000-0000-000000000000/resourceGroups/testGroup"
	vnetID := resourceGroupID + "/providers/Microsoft.Network/virtualNetworks/testVnet"
	genericSubnet := vnetID + "/subnet/genericSubnet"
	routeTableID := resourceGroupID + "/providers/Microsoft.Network/routeTables/testRouteTable"

	for _, tt := range []struct {
		name       string
		modifyVnet func(*mgmtnetwork.VirtualNetwork)
		wantErr    string
	}{
		{
			name: "pass",
		},
		{
			name: "pass: no route table",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].RouteTable = nil
			},
		},
		{
			name: "fail: can't find subnet",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				vnet.Subnets = nil
			},
			wantErr: "400: InvalidLinkedVNet: : The subnet '" + genericSubnet + "' could not be found.",
		},
	} {
		vnet := &mgmtnetwork.VirtualNetwork{
			ID: &vnetID,
			VirtualNetworkPropertiesFormat: &mgmtnetwork.VirtualNetworkPropertiesFormat{
				Subnets: &[]mgmtnetwork.Subnet{
					{
						ID: &genericSubnet,
						SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
							RouteTable: &mgmtnetwork.RouteTable{
								ID: &routeTableID,
							},
						},
					},
				},
			},
		}

		if tt.modifyVnet != nil {
			tt.modifyVnet(vnet)
		}

		// purposefully hardcoding path to "" so it is not needed in the wantErr message
		_, err := getRouteTableID(vnet, "", genericSubnet)
		if err != nil && err.Error() != tt.wantErr ||
			err == nil && tt.wantErr != "" {
			t.Error(err)
		}
	}
}

func TestValidateRouteTablePermissions(t *testing.T) {
	ctx := context.Background()

	resourceGroupName := "testGroup"
	resourceGroupID := "/subscriptions/0000000-0000-0000-0000-000000000000/resourceGroups/" + resourceGroupName
	routeTableName := "testRouteTable"
	routeTableID := resourceGroupID + "/providers/Microsoft.Network/routeTables/" + routeTableName

	controller := gomock.NewController(t)
	defer controller.Finish()

	for _, tt := range []struct {
		name    string
		rtID    string
		mocks   func(*mock_authorization.MockPermissionsClient, func())
		wantErr string
	}{
		{
			name: "pass",
			rtID: routeTableID,
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, "Microsoft.Network", "", "routeTables", routeTableName).
					Return([]mgmtauthorization.Permission{
						{
							Actions: &[]string{
								"Microsoft.Network/routeTables/join/action",
								"Microsoft.Network/routeTables/read",
								"Microsoft.Network/routeTables/write",
							},
							NotActions: &[]string{},
						},
					}, nil)
			},
		},
		{
			name:    "fail: cannot parse resource id",
			rtID:    "invalid_route_table_id",
			wantErr: "parsing failed for invalid_route_table_id. Invalid resource Id format",
		},
		{
			name: "fail: missing permissions",
			rtID: routeTableID,
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, "Microsoft.Network", "", "routeTables", routeTableName).
					Do(func(arg0, arg1, arg2, arg3, arg4, arg5 interface{}) {
						cancel()
					}).
					Return([]mgmtauthorization.Permission{
						{
							Actions:    &[]string{},
							NotActions: &[]string{},
						},
					}, nil)
			},
			wantErr: "400: InvalidResourceProviderPermissions: : The resource provider does not have Network Contributor permission on route table '" + routeTableID + "'.",
		},
		{
			name: "fail: not found",
			rtID: routeTableID,
			mocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), resourceGroupName, "Microsoft.Network", "", "routeTables", routeTableName).
					Do(func(arg0, arg1, arg2, arg3, arg4, arg5 interface{}) {
						cancel()
					}).
					Return(
						nil,
						autorest.DetailedError{
							StatusCode: http.StatusNotFound,
						},
					)
			},
			wantErr: "400: InvalidLinkedRouteTable: : The route table '" + routeTableID + "' could not be found.",
		},
	} {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		permissionsClient := mock_authorization.NewMockPermissionsClient(controller)
		if tt.mocks != nil {
			tt.mocks(permissionsClient, cancel)
		}

		dv := &dynamic{
			log:         logrus.NewEntry(logrus.StandardLogger()),
			permissions: permissionsClient,
			code:        "InvalidResourceProviderPermissions",
			typ:         "resource provider",
		}

		// purposefully hardcoding path to "" so it is not needed in the wantErr message
		err := dv.validateRouteTablePermissions(ctx, tt.rtID, "")
		if err != nil && err.Error() != tt.wantErr ||
			err == nil && tt.wantErr != "" {
			t.Error(err)
		}
	}
}

func TestValidateRouteTablesPermissions(t *testing.T) {
	ctx := context.Background()

	subscriptionID := "0000000-0000-0000-0000-000000000000"
	resourceGroupName := "testGroup"
	resourceGroupID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroupName
	vnetName := "testVnet"
	vnetID := resourceGroupID + "/providers/Microsoft.Network/virtualNetworks/" + vnetName
	masterSubnet := vnetID + "/subnet/masterSubnet"
	workerSubnet := vnetID + "/subnet/workerSubnet"
	masterRtID := resourceGroupID + "/providers/Microsoft.Network/routeTables/masterRt"
	workerRtID := resourceGroupID + "/providers/Microsoft.Network/routeTables/workerRt"

	controller := gomock.NewController(t)
	defer controller.Finish()

	for _, tt := range []struct {
		name            string
		permissionMocks func(*mock_authorization.MockPermissionsClient, func())
		vnetMocks       func(*mock_network.MockVirtualNetworksClient, mgmtnetwork.VirtualNetwork)
		wantErr         string
	}{
		{
			name: "fail: failed to get vnet",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, errors.New("failed to get vnet"))
			},
			wantErr: "failed to get vnet",
		},
		{
			name: "fail: master subnet doesn't exist",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnet.Subnets = nil
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
			wantErr: "400: InvalidLinkedVNet: properties.masterProfile.subnetId: The subnet '" + masterSubnet + "' could not be found.",
		},
		{
			name: "fail: worker subnet ID doesn't exist",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[1].ID = to.StringPtr("not valid")
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
			wantErr: "400: InvalidLinkedVNet: properties.workerProfiles[0].subnetId: The subnet '" + workerSubnet + "' could not be found.",
		},
		{
			name: "fail: permissions don't exist",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
			permissionMocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), strings.ToLower(resourceGroupName), strings.ToLower("Microsoft.Network"), "", strings.ToLower("routeTables"), gomock.Any()).
					Do(func(arg0, arg1, arg2, arg3, arg4, arg5 interface{}) {
						cancel()
					}).
					Return(
						[]mgmtauthorization.Permission{
							{
								Actions:    &[]string{},
								NotActions: &[]string{},
							},
						},
						nil,
					)
			},
			wantErr: "400: InvalidResourceProviderPermissions: : The resource provider does not have Network Contributor permission on route table '" + strings.ToLower(masterRtID) + "'.",
		},
		{
			name: "pass",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
			permissionMocks: func(permissionsClient *mock_authorization.MockPermissionsClient, cancel func()) {
				permissionsClient.EXPECT().
					ListForResource(gomock.Any(), strings.ToLower(resourceGroupName), strings.ToLower("Microsoft.Network"), "", strings.ToLower("routeTables"), gomock.Any()).
					AnyTimes().
					Return([]mgmtauthorization.Permission{
						{
							Actions: &[]string{
								"Microsoft.Network/routeTables/join/action",
								"Microsoft.Network/routeTables/read",
								"Microsoft.Network/routeTables/write",
							},
							NotActions: &[]string{},
						},
					}, nil)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			permissionsClient := mock_authorization.NewMockPermissionsClient(controller)
			vnetClient := mock_network.NewMockVirtualNetworksClient(controller)

			vnet := &mgmtnetwork.VirtualNetwork{
				ID: &vnetID,
				VirtualNetworkPropertiesFormat: &mgmtnetwork.VirtualNetworkPropertiesFormat{
					Subnets: &[]mgmtnetwork.Subnet{
						{
							ID: &masterSubnet,
							SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
								RouteTable: &mgmtnetwork.RouteTable{
									ID: &masterRtID,
								},
							},
						},
						{
							ID: &workerSubnet,
							SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
								RouteTable: &mgmtnetwork.RouteTable{
									ID: &workerRtID,
								},
							},
						},
					},
				},
			}

			dv := &dynamic{
				log:             logrus.NewEntry(logrus.StandardLogger()),
				permissions:     permissionsClient,
				virtualNetworks: vnetClient,

				vnetr: &azure.Resource{
					ResourceGroup:  resourceGroupName,
					ResourceName:   vnetName,
					SubscriptionID: subscriptionID,
					Provider:       "Microsoft.Network",
					ResourceType:   "virtualNetworks",
				},

				masterSubnetID:  masterSubnet,
				workerSubnetIDs: []string{workerSubnet},

				code: "InvalidResourceProviderPermissions",
				typ:  "resource provider",
			}

			if tt.permissionMocks != nil {
				tt.permissionMocks(permissionsClient, cancel)
			}

			if tt.vnetMocks != nil {
				tt.vnetMocks(vnetClient, *vnet)
			}

			err := dv.ValidateRouteTablesPermissions(ctx)
			if err != nil && err.Error() != tt.wantErr ||
				err == nil && tt.wantErr != "" {
				t.Error(err)
			}
		})
	}
}

func TestValidateCIDRRanges(t *testing.T) {
	ctx := context.Background()

	resourceGroupName := "testGroup"
	resourceGroupID := "/subscriptions/0000000-0000-0000-0000-000000000000/resourceGroups/" + resourceGroupName
	vnetName := "testVnet"
	subscriptionID := "0000000-0000-0000-0000-000000000000"
	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Network/virtualNetworks/" + vnetName
	masterSubnet := vnetID + "/subnet/masterSubnet"
	workerSubnet := vnetID + "/subnet/workerSubnet"
	masterNSGv1 := resourceGroupID + "/providers/Microsoft.Network/networkSecurityGroups/aro-controlplane-nsg"
	workerNSGv1 := resourceGroupID + "/providers/Microsoft.Network/networkSecurityGroups/aro-node-nsg"

	controller := gomock.NewController(t)
	defer controller.Finish()

	for _, tt := range []struct {
		name      string
		modifyOC  func(*api.OpenShiftCluster)
		vnetMocks func(*mock_network.MockVirtualNetworksClient, mgmtnetwork.VirtualNetwork)
		wantErr   string
	}{
		{
			name: "pass",
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
		},
		{
			name: "fail: conflicting ranges",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.NetworkProfile.ServiceCIDR = "10.0.0.0/24"
			},
			vnetMocks: func(vnetClient *mock_network.MockVirtualNetworksClient, vnet mgmtnetwork.VirtualNetwork) {
				vnetClient.EXPECT().
					Get(gomock.Any(), resourceGroupName, vnetName, "").
					Return(vnet, nil)
			},
			wantErr: "400: InvalidLinkedVNet: : The provided CIDRs must not overlap: '10.0.0.0/24 overlaps with 10.0.0.0/24'.",
		},
	} {
		oc := &api.OpenShiftCluster{
			Properties: api.OpenShiftClusterProperties{
				ClusterProfile: api.ClusterProfile{
					ResourceGroupID: resourceGroupID,
				},
				NetworkProfile: api.NetworkProfile{
					PodCIDR:     "10.0.2.0/24",
					ServiceCIDR: "10.0.3.0/24",
				},
				MasterProfile: api.MasterProfile{
					SubnetID: masterSubnet,
				},
				WorkerProfiles: []api.WorkerProfile{
					{
						SubnetID: workerSubnet,
					},
					{
						SubnetID: workerSubnet,
					},
				},
			},
		}

		vnet := mgmtnetwork.VirtualNetwork{
			ID:       &vnetID,
			Location: to.StringPtr("eastus"),
			VirtualNetworkPropertiesFormat: &mgmtnetwork.VirtualNetworkPropertiesFormat{
				Subnets: &[]mgmtnetwork.Subnet{
					{
						ID: &masterSubnet,
						SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
							AddressPrefix: to.StringPtr("10.0.0.0/24"),
							NetworkSecurityGroup: &mgmtnetwork.SecurityGroup{
								ID: &masterNSGv1,
							},
							ServiceEndpoints: &[]mgmtnetwork.ServiceEndpointPropertiesFormat{
								{
									Service:           to.StringPtr("Microsoft.ContainerRegistry"),
									ProvisioningState: mgmtnetwork.Succeeded,
								},
							},
							PrivateLinkServiceNetworkPolicies: to.StringPtr("Disabled"),
						},
					},
					{
						ID: &workerSubnet,
						SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
							AddressPrefix: to.StringPtr("10.0.1.0/24"),
							NetworkSecurityGroup: &mgmtnetwork.SecurityGroup{
								ID: &workerNSGv1,
							},
							ServiceEndpoints: &[]mgmtnetwork.ServiceEndpointPropertiesFormat{
								{
									Service:           to.StringPtr("Microsoft.ContainerRegistry"),
									ProvisioningState: mgmtnetwork.Succeeded,
								},
							},
						},
					},
				},
			},
		}

		if tt.modifyOC != nil {
			tt.modifyOC(oc)
		}

		vnetClient := mock_network.NewMockVirtualNetworksClient(controller)
		if tt.vnetMocks != nil {
			tt.vnetMocks(vnetClient, vnet)
		}

		vnetr, err := azure.ParseResourceID(vnetID)
		if err != nil {
			t.Error(err)
		}

		dv := &dynamic{
			oc:              oc,
			vnetr:           &vnetr,
			log:             logrus.NewEntry(logrus.StandardLogger()),
			virtualNetworks: vnetClient,
		}

		err = dv.ValidateCIDRRanges(ctx)
		if err != nil && err.Error() != tt.wantErr ||
			err == nil && tt.wantErr != "" {
			t.Error(err)
		}
	}
}

func TestValidateVnetLocation(t *testing.T) {
	ctx := context.Background()

	controller := gomock.NewController(t)
	defer controller.Finish()

	resourceGroupName := "testGroup"
	vnetName := "testVnet"
	vnetID := "/subscriptions/0000000-0000-0000-0000-000000000000/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Network/virtualNetworks/" + vnetName

	for _, tt := range []struct {
		name     string
		location string
		wantErr  string
	}{
		{
			name:     "pass",
			location: "eastus",
		},
		{
			name:     "fail: location differs",
			location: "neverland",
			wantErr:  "400: InvalidLinkedVNet: : The vnet location 'neverland' must match the cluster location 'eastus'.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {

			vnet := mgmtnetwork.VirtualNetwork{
				ID:       to.StringPtr(vnetID),
				Location: to.StringPtr(tt.location),
			}

			vnetClient := mock_network.NewMockVirtualNetworksClient(controller)
			vnetClient.EXPECT().
				Get(gomock.Any(), resourceGroupName, vnetName, "").
				Return(vnet, nil)

			vnetr, err := azure.ParseResourceID(vnetID)
			if err != nil {
				t.Error(err)
			}

			oc := &api.OpenShiftCluster{
				Location: "eastus",
			}

			dv := &dynamic{
				oc:              oc,
				vnetr:           &vnetr,
				log:             logrus.NewEntry(logrus.StandardLogger()),
				virtualNetworks: vnetClient,
			}

			err = dv.ValidateVnetLocation(ctx)
			if err != nil && err.Error() != tt.wantErr ||
				err == nil && tt.wantErr != "" {
				t.Error(err)
			}
		})
	}
}

func TestValidateSubnet(t *testing.T) {
	ctx := context.Background()

	resourceGroupID := "/subscriptions/0000000-0000-0000-0000-000000000000/resourceGroups/testGroup"
	vnetID := resourceGroupID + "/providers/Microsoft.Network/virtualNetworks/testVnet"
	genericSubnet := vnetID + "/subnet/genericSubnet"
	masterNSGv1 := resourceGroupID + "/providers/Microsoft.Network/networkSecurityGroups/aro-controlplane-nsg"

	for _, tt := range []struct {
		name       string
		modifyOC   func(*api.OpenShiftCluster)
		modifyVnet func(*mgmtnetwork.VirtualNetwork)
		wantErr    string
	}{
		{
			name: "pass",
		},
		{
			name: "pass (master subnet)",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.MasterProfile = api.MasterProfile{
					SubnetID: genericSubnet,
				}
			},
		},
		{
			name: "pass (cluster in creating provisioning status)",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.ProvisioningState = api.ProvisioningStateCreating
			},
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].NetworkSecurityGroup = nil
			},
		},
		{
			name: "fail: subnet doe not exist on vnet",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				vnet.Subnets = nil
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' could not be found.",
		},
		{
			name: "fail: private link service network policies enabled on master subnet",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.MasterProfile = api.MasterProfile{
					SubnetID: genericSubnet,
				}
			},
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].PrivateLinkServiceNetworkPolicies = to.StringPtr("Enabled")
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must have privateLinkServiceNetworkPolicies disabled.",
		},
		{
			name: "fail: container registry endpoint doesn't exist",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].ServiceEndpoints = nil
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must have Microsoft.ContainerRegistry serviceEndpoint.",
		},
		{
			name: "fail: network provisioning state not succeeded",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*(*vnet.Subnets)[0].ServiceEndpoints)[0].ProvisioningState = mgmtnetwork.Failed
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must have Microsoft.ContainerRegistry serviceEndpoint.",
		},
		{
			name: "fail: provisioning state creating: subnet has NSG",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.ProvisioningState = api.ProvisioningStateCreating
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must not have a network security group attached.",
		},
		{
			name: "fail: invalid architecture version returns no NSG",
			modifyOC: func(oc *api.OpenShiftCluster) {
				oc.Properties.ArchitectureVersion = 9001
			},
			wantErr: "unknown architecture version 9001",
		},
		{
			name: "fail: nsg id doesn't match expected",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].NetworkSecurityGroup.ID = to.StringPtr("not matching")
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must have network security group '" + masterNSGv1 + "' attached.",
		},
		{
			name: "fail: invalid subnet CIDR",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].AddressPrefix = to.StringPtr("not-valid")
			},
			wantErr: "invalid CIDR address: not-valid",
		},
		{
			name: "fail: too small subnet CIDR",
			modifyVnet: func(vnet *mgmtnetwork.VirtualNetwork) {
				(*vnet.Subnets)[0].AddressPrefix = to.StringPtr("10.0.0.0/28")
			},
			wantErr: "400: InvalidLinkedVNet: : The provided subnet '" + genericSubnet + "' is invalid: must be /27 or larger.",
		},
	} {
		oc := &api.OpenShiftCluster{
			Properties: api.OpenShiftClusterProperties{
				ClusterProfile: api.ClusterProfile{
					ResourceGroupID: resourceGroupID,
				},
			},
		}
		vnet := &mgmtnetwork.VirtualNetwork{
			ID: &vnetID,
			VirtualNetworkPropertiesFormat: &mgmtnetwork.VirtualNetworkPropertiesFormat{
				Subnets: &[]mgmtnetwork.Subnet{
					{
						ID: &genericSubnet,
						SubnetPropertiesFormat: &mgmtnetwork.SubnetPropertiesFormat{
							AddressPrefix: to.StringPtr("10.0.0.0/24"),
							NetworkSecurityGroup: &mgmtnetwork.SecurityGroup{
								ID: &masterNSGv1,
							},
							ServiceEndpoints: &[]mgmtnetwork.ServiceEndpointPropertiesFormat{
								{
									Service:           to.StringPtr("Microsoft.ContainerRegistry"),
									ProvisioningState: mgmtnetwork.Succeeded,
								},
							},
							PrivateLinkServiceNetworkPolicies: to.StringPtr("Disabled"),
						},
					},
				},
			},
		}

		if tt.modifyOC != nil {
			tt.modifyOC(oc)
		}
		if tt.modifyVnet != nil {
			tt.modifyVnet(vnet)
		}

		dv := &dynamic{
			log: logrus.NewEntry(logrus.StandardLogger()),
			oc:  oc,
		}

		// purposefully hardcoding path to "" so it is not needed in the wantErr message
		_, err := dv.validateSubnet(ctx, vnet, "", genericSubnet)
		if err != nil && err.Error() != tt.wantErr ||
			err == nil && tt.wantErr != "" {
			t.Error(err)
		}
	}
}

func TestValidateProviders(t *testing.T) {
	ctx := context.Background()

	controller := gomock.NewController(t)
	defer controller.Finish()

	for _, tt := range []struct {
		name    string
		mocks   func(*mock_features.MockProvidersClient)
		wantErr string
	}{
		{
			name: "pass",
			mocks: func(providersClient *mock_features.MockProvidersClient) {
				providersClient.EXPECT().
					List(gomock.Any(), nil, "").
					Return([]mgmtfeatures.Provider{
						{
							Namespace:         to.StringPtr("Microsoft.Authorization"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Compute"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Network"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Storage"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherRegisteredProvider"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherNotRegisteredProvider"),
							RegistrationState: to.StringPtr("NotRegistered"),
						},
					}, nil)
			},
		},
		{
			name: "fail: compute not registered",
			mocks: func(providersClient *mock_features.MockProvidersClient) {
				providersClient.EXPECT().
					List(gomock.Any(), nil, "").
					Return([]mgmtfeatures.Provider{
						{
							Namespace:         to.StringPtr("Microsoft.Authorization"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Compute"),
							RegistrationState: to.StringPtr("NotRegistered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Network"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Storage"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherRegisteredProvider"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherNotRegisteredProvider"),
							RegistrationState: to.StringPtr("NotRegistered"),
						},
					}, nil)
			},
			wantErr: "400: ResourceProviderNotRegistered: : The resource provider 'Microsoft.Compute' is not registered.",
		},
		{
			name: "fail: storage missing",
			mocks: func(providersClient *mock_features.MockProvidersClient) {
				providersClient.EXPECT().
					List(gomock.Any(), nil, "").
					Return([]mgmtfeatures.Provider{
						{
							Namespace:         to.StringPtr("Microsoft.Authorization"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Compute"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("Microsoft.Network"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherRegisteredProvider"),
							RegistrationState: to.StringPtr("Registered"),
						},
						{
							Namespace:         to.StringPtr("otherNotRegisteredProvider"),
							RegistrationState: to.StringPtr("NotRegistered"),
						},
					}, nil)
			},
			wantErr: "400: ResourceProviderNotRegistered: : The resource provider 'Microsoft.Storage' is not registered.",
		},
		{
			name: "error case",
			mocks: func(providersClient *mock_features.MockProvidersClient) {
				providersClient.EXPECT().
					List(gomock.Any(), nil, "").
					Return(nil, errors.New("random error"))
			},
			wantErr: "random error",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			providerClient := mock_features.NewMockProvidersClient(controller)

			tt.mocks(providerClient)

			dv := &dynamic{
				log:       logrus.NewEntry(logrus.StandardLogger()),
				providers: providerClient,
			}

			err := dv.ValidateProviders(ctx)
			if err != nil && err.Error() != tt.wantErr ||
				err == nil && tt.wantErr != "" {
				t.Error(err)
			}
		})
	}
}
