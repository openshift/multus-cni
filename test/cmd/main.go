package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"

	"github.com/spf13/cobra"

	exutil "github.com/openshift/origin/test/extended/util"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	_ "gopkg.in/k8snetworkplumbingwg/multus-cni.v4/test/ote"
)

func main() {
	registry := e.NewRegistry()

	ext := e.NewExtension("openshift", "payload", "multus-cni")

	// Disruptive tests (e.g. node reboot) run one at a time under the disruptive
	// cluster-stability classification — same pattern as cluster-network-operator.
	ext.AddSuite(e.Suite{
		Name:             "openshift/multus-cni/disruptive",
		Qualifiers:       []string{`labels.exists(l, l == "Disruptive")`},
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
	})
	ext.AddSuite(e.Suite{
		Name: "openshift/multus-cni",
		Parents: []string{
			"openshift/conformance/parallel",
		},
		Qualifiers: []string{
			`!labels.exists(l, l == "Disruptive")`,
		},
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't build extension test specs from ginkgo: %v\n", err)
		os.Exit(1)
	}

	cfg, cfgErr := getKubeConfig()

	specs.AddBeforeAll(func() {
		if cfgErr != nil {
			panic(cfgErr)
		}
		if err := initializeTestFramework(os.Getenv("TEST_PROVIDER"), cfg); err != nil {
			panic(err)
		}
		exutil.WithCleanup(func() {})
	})

	specs.Walk(func(spec *et.ExtensionTestSpec) {
		spec.Lifecycle = et.LifecycleInforming
	})
	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension for Multus CNI",
	}
	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
