package cluster

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Azure/ARO-RP/pkg/api"
	mock_metrics "github.com/Azure/ARO-RP/pkg/util/mocks/metrics"
	utiltls "github.com/Azure/ARO-RP/pkg/util/tls"
	utilerror "github.com/Azure/ARO-RP/test/util/error"
)

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.
type certInfo struct {
	secretName, certSubject string
}

const (
	managedDomainName   = "contoso.aroapp.io"
	unmanagedDomainName = "aro.contoso.com"
)

func TestEmitCertificateExpirationStatuses(t *testing.T) {
	expiration := time.Now().Add(time.Hour * 24 * 5)
	expirationString := expiration.UTC().Format(time.RFC3339)
	for _, tt := range []struct {
		name            string
		domain          string
		certsPresent    []certInfo
		wantExpirations []map[string]string
		wantErr         string
	}{
		{
			name:         "only emits MDSD status for unmanaged domain",
			domain:       unmanagedDomainName,
			certsPresent: []certInfo{{"cluster", "geneva.certificate"}},
			wantExpirations: []map[string]string{
				{
					"subject":        "geneva.certificate",
					"expirationDate": expirationString,
				},
			},
		},
		{
			name:   "includes ingress and API status for managed domain",
			domain: managedDomainName,
			certsPresent: []certInfo{
				{"cluster", "geneva.certificate"},
				{"foo12-ingress", managedDomainName},
				{"foo12-apiserver", "api." + managedDomainName},
			},
			wantExpirations: []map[string]string{
				{
					"subject":        "geneva.certificate",
					"expirationDate": expirationString,
				},
				{
					"subject":        "contoso.aroapp.io",
					"expirationDate": expirationString,
				},
				{
					"subject":        "api.contoso.aroapp.io",
					"expirationDate": expirationString,
				},
			},
		},
		{
			name:    "returns error when cluster secret has been deleted",
			domain:  unmanagedDomainName,
			wantErr: `secrets "cluster" not found`,
		},
		{
			name:   "returns error when managed domain secret has been deleted",
			domain: managedDomainName,
			certsPresent: []certInfo{
				{"cluster", "geneva.certificate"},
				{"foo12-ingress", managedDomainName},
			},
			wantErr: `secrets "foo12-apiserver" not found`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			var secrets []runtime.Object
			secretsFromCertInfo, err := generateTestSecrets(tt.certsPresent, tweakTemplateFn(expiration))
			if err != nil {
				t.Fatal(err)
			}
			secrets = append(secrets, secretsFromCertInfo...)

			m := mock_metrics.NewMockEmitter(gomock.NewController(t))
			for _, gauge := range tt.wantExpirations {
				m.EXPECT().EmitGauge("certificate.expirationdate", int64(1), gauge)
			}

			mon := buildMonitor(m, tt.domain, secrets...)

			err = mon.emitCertificateExpirationStatuses(ctx)

			utilerror.AssertErrorMessage(t, err, tt.wantErr)
		})
	}

	t.Run("returns error when secret is present but certificate data has been deleted", func(t *testing.T) {
		var secrets []runtime.Object
		data := map[string][]byte{}
		s := buildSecret("cluster", data)
		secrets = append(secrets, s)

		ctx := context.Background()
		m := mock_metrics.NewMockEmitter(gomock.NewController(t))
		mon := buildMonitor(m, managedDomainName, secrets...)

		wantErr := `certificate "gcscert.pem" not found on secret "cluster"`
		err := mon.emitCertificateExpirationStatuses(ctx)
		utilerror.AssertErrorMessage(t, err, wantErr)
	})
}

func tweakTemplateFn(expiration time.Time) func(*x509.Certificate) {
	return func(template *x509.Certificate) {
		template.NotAfter = expiration
	}
}

func generateTestSecrets(certsInfo []certInfo, tweakTemplateFn func(*x509.Certificate)) ([]runtime.Object, error) {
	var secrets []runtime.Object
	for _, sec := range certsInfo {
		_, cert, err := utiltls.GenerateTestKeyAndCertificate(sec.certSubject, nil, nil, false, false, tweakTemplateFn)
		if err != nil {
			return nil, err
		}
		certKey := "tls.crt"
		if sec.secretName == "cluster" {
			certKey = "gcscert.pem"
		}
		data := map[string][]byte{
			certKey: pem.EncodeToMemory(&pem.Block{
				Type:  "CERTIFICATE",
				Bytes: cert[0].Raw,
			}),
		}
		s := buildSecret(sec.secretName, data)
		secrets = append(secrets, s)
	}
	return secrets, nil
}

func buildSecret(secretName string, data map[string][]byte) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "openshift-azure-operator",
		},
		Data: data,
	}
	return s
}

func buildMonitor(m *mock_metrics.MockEmitter, domain string, secrets ...runtime.Object) *Monitor {
	mon := &Monitor{
		cli: fake.NewSimpleClientset(secrets...),
		m:   m,
		oc: &api.OpenShiftCluster{
			Properties: api.OpenShiftClusterProperties{
				ClusterProfile: api.ClusterProfile{
					Domain: domain,
				},
				InfraID: "foo12",
			},
		},
	}
	return mon
}
