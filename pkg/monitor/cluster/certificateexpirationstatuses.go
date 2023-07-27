package cluster

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Azure/ARO-RP/pkg/operator"
	"github.com/Azure/ARO-RP/pkg/operator/controllers/genevalogging"
	"github.com/Azure/ARO-RP/pkg/util/dns"
)

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.
const (
	certificateExpirationMetricName = "certificate.expirationdate"
	secretMissingMetricName         = "certificate.secretnotfound"
)

func (mon *Monitor) emitCertificateExpirationStatuses(ctx context.Context) error {
	// report NotAfter dates for Ingress and API (on managed domains), and Geneva (always)
	var certs []*x509.Certificate

	mdsdCert, err := mon.getCertificate(ctx, operator.Namespace, operator.SecretName, genevalogging.GenevaCertName)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			return err
		}
		mon.emitGauge(secretMissingMetricName, int64(1), map[string]string{
			"secretMissing": operator.SecretName,
		})
	} else {
		certs = append(certs, mdsdCert)
	}

	if dns.IsManagedDomain(mon.oc.Properties.ClusterProfile.Domain) {
		ingressController, err := mon.operatorcli.OperatorV1().IngressControllers("openshift-ingress-operator").Get(ctx, "default", metav1.GetOptions{})
		if err != nil {
			return err
		}
		ingressSecretName := ingressController.Spec.DefaultCertificate.Name
		for _, secretName := range []string{ingressSecretName, strings.Replace(ingressSecretName, "-ingress", "-apiserver", 1)} { // certificate name is uuid + "-ingress" or "-apiserver"
			certificate, err := mon.getCertificate(ctx, operator.Namespace, secretName, corev1.TLSCertKey)
			if kerrors.IsNotFound(err) {
				mon.emitGauge(secretMissingMetricName, int64(1), map[string]string{
					"secretMissing": secretName,
				})
			} else if err != nil {
				return err
			} else {
				certs = append(certs, certificate)
			}
		}
	}

	for _, cert := range certs {
		mon.emitGauge(certificateExpirationMetricName, 1, map[string]string{
			"subject":        cert.Subject.CommonName,
			"expirationDate": cert.NotAfter.UTC().Format(time.RFC3339),
		})
	}
	return nil
}

func (mon *Monitor) getCertificate(ctx context.Context, secretNamespace, secretName, secretKey string) (*x509.Certificate, error) {
	secret, err := mon.cli.CoreV1().Secrets(secretNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	certBlock, _ := pem.Decode(secret.Data[secretKey])
	if certBlock == nil {
		return nil, fmt.Errorf(`certificate "%s" not found on secret "%s"`, secretKey, secretName)
	}
	// we only care about the first certificate in the block
	return x509.ParseCertificate(certBlock.Bytes)
}
