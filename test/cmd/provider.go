package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	kclientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/test/e2e/framework"

	"github.com/onsi/gomega"
)

func initializeTestFramework(provider string, cfg *restclient.Config) error {
	if len(provider) == 0 {
		provider = "{\"type\":\"skeleton\"}"
	}
	config := &ClusterConfiguration{}
	if err := json.Unmarshal([]byte(provider), config); err != nil {
		return fmt.Errorf("provider must be a JSON object with the 'type' key at a minimum: %v", err)
	}
	if len(config.ProviderName) == 0 {
		return fmt.Errorf("provider must be a JSON object with the 'type' key")
	}

	framework.TestContext.Provider = config.ProviderName
	framework.TestContext.CloudConfig = framework.CloudConfig{
		ProjectID:   config.ProjectID,
		Region:      config.Region,
		Zone:        config.Zone,
		Zones:       config.Zones,
		NumNodes:    config.NumNodes,
		MultiMaster: config.MultiMaster,
		MultiZone:   config.MultiZone,
		ConfigFile:  config.ConfigFile,
		Provider:    framework.NullProvider{},
	}
	framework.TestContext.AllowedNotReadyNodes = 0
	framework.TestContext.MinStartupPods = -1
	framework.TestContext.MaxNodesToGather = 0
	framework.TestContext.KubeConfig = os.Getenv("KUBECONFIG")
	gomega.Expect(framework.TestContext.KubeConfig).NotTo(gomega.BeEmpty())
	framework.TestContext.DeleteNamespace = os.Getenv("DELETE_NAMESPACE") != "false"
	framework.TestContext.VerifyServiceAccount = true
	framework.TestContext.KubectlPath = "oc"
	if ad := os.Getenv("ARTIFACT_DIR"); len(strings.TrimSpace(ad)) == 0 {
		os.Setenv("ARTIFACT_DIR", filepath.Join(os.TempDir(), "artifacts"))
	}
	framework.TestContext.NodeOSDistro = "custom"
	framework.TestContext.MasterOSDistro = "custom"
	gomega.Expect(cfg).NotTo(gomega.BeNil())
	framework.TestContext.Host = cfg.Host
	framework.TestContext.CreateTestingNS = func(ctx context.Context, baseName string, c kclientset.Interface, labels map[string]string) (*corev1.Namespace, error) {
		return framework.CreateTestingNS(ctx, baseName, c, labels)
	}
	framework.TestContext.DumpLogsOnFailure = true
	framework.TestContext.ReportDir = os.Getenv("TEST_JUNIT_DIR")
	return nil
}

func getKubeConfig() (*restclient.Config, error) {
	kubeConfig := os.Getenv("KUBECONFIG")
	if kubeConfig == "" {
		return nil, fmt.Errorf("KUBECONFIG env variable not set")
	}
	if _, err := os.Stat(kubeConfig); err != nil {
		return nil, fmt.Errorf("KUBECONFIG file %q not accessible: %w", kubeConfig, err)
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfig},
		&clientcmd.ConfigOverrides{})
	return clientConfig.ClientConfig()
}
