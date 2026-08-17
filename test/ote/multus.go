package ote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	exutil "github.com/openshift/origin/test/extended/util"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	imageHelloSDN = "quay.io/openshifttest/hello-sdn@sha256:c89445416459e7adea9a5a416b3365ed3d74f2491beb904d61dc8d1eb89a72a4"
	imageUBIMin   = "registry.access.redhat.com/ubi8/ubi-minimal:latest"
	imagePython39 = "registry.access.redhat.com/ubi9/python-39:latest"
)

var _ = g.Describe("[sig-network][OTP][Suite:openshift/conformance/parallel] Multus CNI", func() {
	var (
		oc        = exutil.NewCLI("multus-cni")
		clientset kubernetes.Interface
		config    *rest.Config
		ctx       context.Context
	)

	g.BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
		g.DeferCleanup(cancel)

		config = oc.AdminConfig()
		clientset = oc.AdminKubeClient()
	})

	// High-57589: Whereabouts CNI Timeout with Large Exclude Range
	g.It("[JIRA:Networking][OTP] 57589-should handle large IPv6 exclude ranges without timeout", func() {
		testNS := fmt.Sprintf("test-whereabouts-57589-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			g.By("Cleaning up test namespace")
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NetworkAttachmentDefinition with large exclude range")
		nadConfig := `{
      "cniVersion": "0.3.1",
      "name": "bridge-net",
      "type": "bridge",
      "bridge": "test-br0",
      "isGateway": false,
      "ipMasq": false,
      "ipam": {
         "type": "whereabouts",
         "range": "fd43:01f1:3daa:0baa::/64",
         "exclude": [ "fd43:01f1:3daa:0baa::/100" ],
         "log_file": "/tmp/whereabouts.log",
         "log_level" : "debug"
      }
    }`

		err = createNAD(ctx, config, testNS, "nad-w-excludes", nadConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating pod with secondary network")
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: testNS,
				Annotations: map[string]string{
					"k8s.v1.cni.cncf.io/networks": "nad-w-excludes",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    "test",
						Image:   imageUBIMin,
						Command: []string{"sleep", "3600"},
					},
				},
			},
		}

		_, err = clientset.CoreV1().Pods(testNS).Create(ctx, pod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for pod to reach Running state (max 60s)")
		// Pod should be Running within 60 seconds (test validates no timeout)
		o.Eventually(func() corev1.PodPhase {
			p, err := clientset.CoreV1().Pods(testNS).Get(ctx, "test-pod", metav1.GetOptions{})
			if err != nil {
				return corev1.PodPending
			}
			return p.Status.Phase
		}, 60, 5).Should(o.Equal(corev1.PodRunning),
			"Pod did not reach Running state within 60s - Whereabouts may have timed out")

		g.By("Verifying secondary network attachment")
		p, err := clientset.CoreV1().Pods(testNS).Get(ctx, "test-pod", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		networkStatus, ok := p.Annotations["k8s.v1.cni.cncf.io/network-status"]
		o.Expect(ok).To(o.BeTrue(), "Pod missing network-status annotation")
		o.Expect(networkStatus).NotTo(o.BeEmpty())

		// Verify at least 2 networks (primary + secondary)
		networkCount := strings.Count(networkStatus, `"name"`)
		o.Expect(networkCount).To(o.BeNumerically(">=", 2),
			"Expected at least 2 networks, got %d", networkCount)
	})

	// Medium-76652: Dummy CNI Support
	g.It("[JIRA:Networking][OTP] 76652-should support Dummy CNI plugin with Multus", func() {
		testNS := fmt.Sprintf("test-dummy-cni-76652-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			g.By("Cleaning up test namespace")
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NetworkAttachmentDefinition with dummy CNI and static IPAM")
		dummyConfig := `{
      "cniVersion": "0.3.1",
      "name": "dummy-net",
      "type": "dummy",
      "ipam": {
        "type": "static",
        "addresses": [
          {
            "address": "10.10.10.2/24"
          }
        ]
      }
    }`

		err = createNAD(ctx, config, testNS, "dummy-net", dummyConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating pod with dummy network attached")
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-dummy-pod",
				Namespace: testNS,
				Annotations: map[string]string{
					"k8s.v1.cni.cncf.io/networks": "dummy-net",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    "test",
						Image:   imageUBIMin,
						Command: []string{"sleep", "3600"},
					},
				},
			},
		}

		_, err = clientset.CoreV1().Pods(testNS).Create(ctx, pod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for pod to reach Running state")
		o.Eventually(func() corev1.PodPhase {
			p, err := clientset.CoreV1().Pods(testNS).Get(ctx, "test-dummy-pod", metav1.GetOptions{})
			if err != nil {
				return corev1.PodPending
			}
			return p.Status.Phase
		}, 60, 5).Should(o.Equal(corev1.PodRunning),
			"Pod did not reach Running state within 60s")

		g.By("Verifying dummy network interface is created")
		p, err := clientset.CoreV1().Pods(testNS).Get(ctx, "test-dummy-pod", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		networkStatus, ok := p.Annotations["k8s.v1.cni.cncf.io/network-status"]
		o.Expect(ok).To(o.BeTrue(), "Pod missing network-status annotation")
		o.Expect(networkStatus).NotTo(o.BeEmpty())

		g.By("Validating dummy interface has correct IP and configuration")
		// Network status should contain 2 interfaces: ovn-kubernetes (primary) + dummy-net (secondary)
		o.Expect(networkStatus).To(o.ContainSubstring("ovn-kubernetes"), "Should have primary OVN network")
		o.Expect(networkStatus).To(o.ContainSubstring("dummy-net"), "Should have dummy network")
		o.Expect(networkStatus).To(o.ContainSubstring("10.10.10.2"), "Should have assigned dummy IP")

		// Verify we have at least 2 network interfaces
		networkCount := strings.Count(networkStatus, `"name"`)
		o.Expect(networkCount).To(o.BeNumerically(">=", 2),
			"Expected at least 2 networks (primary + dummy), got %d", networkCount)
	})

	// Medium-66876: Support Dual Stack IP assignment for whereabouts CNI/IPAM
	g.It("[JIRA:Networking][OTP] 66876-should assign dual-stack IPs with Whereabouts IPAM", func() {
		testNS := fmt.Sprintf("test-whereabouts-dualstack-66876-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			g.By("Cleaning up test namespace")
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NetworkAttachmentDefinition with dual-stack Whereabouts IPAM")
		dualStackConfig := `{
			"cniVersion": "0.3.1",
			"name": "whereabouts-dualstack",
			"type": "macvlan",
			"mode": "bridge",
			"ipam": {
				"type": "whereabouts",
				"ipRanges": [
					{
						"range": "192.168.10.0/24"
					},
					{
						"range": "fd00:dead:beef:10::/64"
					}
				]
			}
		}`

		err = createNAD(ctx, config, testNS, "whereabouts-dualstack", dualStackConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating deployment with 2 pods using pod affinity for same-node placement")
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: testNS,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(2),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "test-pod",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test-pod",
						},
						Annotations: map[string]string{
							"k8s.v1.cni.cncf.io/networks": "whereabouts-dualstack",
						},
					},
					Spec: corev1.PodSpec{
						Affinity: &corev1.Affinity{
							PodAffinity: &corev1.PodAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": "test-pod",
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name:    "test-pod",
								Image:   imagePython39,
								Command: []string{"/bin/bash", "-c"},
								Args: []string{
									`cat > /tmp/server.py <<'PYEOF'
import http.server
import socketserver
import socket
PORT = 8080
class DualStackTCPServer(socketserver.TCPServer):
    address_family = socket.AF_INET6
    def __init__(self, server_address, RequestHandlerClass, bind_and_activate=True):
        super().__init__(server_address, RequestHandlerClass, bind_and_activate=False)
        self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        if bind_and_activate:
            self.server_bind()
            self.server_activate()
class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-type', 'text/plain')
        self.end_headers()
        self.wfile.write(b'whereabouts-dualstack-test-pod\n')
with DualStackTCPServer(("::", PORT), Handler) as httpd:
    httpd.serve_forever()
PYEOF
python3 /tmp/server.py`,
								},
								Ports: []corev1.ContainerPort{
									{ContainerPort: 8080},
								},
							},
						},
					},
				},
			},
		}

		_, err = clientset.AppsV1().Deployments(testNS).Create(ctx, deployment, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for both pods to reach Running state")
		o.Eventually(func() int {
			pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
				LabelSelector: "app=test-pod",
			})
			if err != nil {
				return 0
			}
			runningCount := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					runningCount++
				}
			}
			return runningCount
		}, 120, 10).Should(o.Equal(2), "Both pods should reach Running state")

		g.By("Verifying pods have dual-stack IPs on secondary interface")
		pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
			LabelSelector: "app=test-pod",
		})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(pods.Items)).To(o.Equal(2), "Should have 2 pods")

		ipv4Assigned := 0
		ipv6Assigned := 0
		var podIPs []string

		for _, pod := range pods.Items {
			networkStatus, ok := pod.Annotations["k8s.v1.cni.cncf.io/network-status"]
			o.Expect(ok).To(o.BeTrue(), "Pod %s should have network-status annotation", pod.Name)

			// Check for dual-stack IPs in network status
			hasIPv4 := strings.Contains(networkStatus, "192.168.10.")
			hasIPv6 := strings.Contains(networkStatus, "fd00:dead:beef:10::")

			if hasIPv4 {
				ipv4Assigned++
			}
			if hasIPv6 {
				ipv6Assigned++
			}

			o.Expect(hasIPv4 && hasIPv6).To(o.BeTrue(),
				"Pod %s should have both IPv4 (192.168.10.x) and IPv6 (fd00:dead:beef:10::x) addresses", pod.Name)

			// Extract IPv4 for uniqueness check
			if hasIPv4 {
				ipv4Regex := regexp.MustCompile(`192\.168\.10\.\d+`)
				matches := ipv4Regex.FindString(networkStatus)
				if matches != "" {
					podIPs = append(podIPs, matches)
				}
			}
		}

		o.Expect(ipv4Assigned).To(o.Equal(2), "Both pods should have IPv4 addresses")
		o.Expect(ipv6Assigned).To(o.Equal(2), "Both pods should have IPv6 addresses")

		g.By("Verifying dual-stack IP uniqueness")
		o.Expect(len(podIPs)).To(o.BeNumerically(">=", 2), "Should have extracted at least 2 IPv4 addresses")

		if len(podIPs) >= 2 {
			o.Expect(podIPs[0]).NotTo(o.Equal(podIPs[1]), "Pods should have different IPv4 addresses")
		}

		g.By("Testing IPv4 connectivity between pods on the same node")
		// Both pods are guaranteed to be on the same node via pod affinity
		// Macvlan in bridge mode requires same-node for L2 connectivity
		o.Expect(len(pods.Items)).To(o.Equal(2), "Should have exactly 2 pods")

		ipv4Regex := regexp.MustCompile(`192\.168\.10\.\d+`)
		ipv6Regex := regexp.MustCompile(`fd00:dead:beef:10::[a-f0-9]+`)

		// Extract IPs from pod 0 and pod 1
		srcPod := pods.Items[0].Name
		networkStatus1 := pods.Items[1].Annotations["k8s.v1.cni.cncf.io/network-status"]

		dstIPv4 := ipv4Regex.FindString(networkStatus1)
		dstIPv6 := ipv6Regex.FindString(networkStatus1)

		o.Expect(dstIPv4).NotTo(o.BeEmpty(), "Pod 1 should have IPv4 address")
		o.Expect(dstIPv6).NotTo(o.BeEmpty(), "Pod 1 should have IPv6 address")

		// Verify both pods are on the same node (should always be true due to affinity)
		o.Expect(pods.Items[0].Spec.NodeName).To(o.Equal(pods.Items[1].Spec.NodeName),
			"Both pods should be on the same node due to pod affinity")

		// Test IPv4 connectivity
		out, err := execInPod(ctx, clientset, config, testNS, srcPod, "test-pod",
			[]string{"curl", "-s", "--connect-timeout", "5", fmt.Sprintf("http://%s:8080", dstIPv4)})
		o.Expect(err).NotTo(o.HaveOccurred(), "IPv4 connectivity test failed: %s", out)
		o.Expect(out).To(o.ContainSubstring("whereabouts-dualstack-test-pod"),
			"IPv4 connectivity: Expected response from hello-sdn server")

		g.By("Testing IPv6 connectivity between pods on secondary network")
		out, err = execInPod(ctx, clientset, config, testNS, srcPod, "test-pod",
			[]string{"curl", "-s", "-6", "-g", "--connect-timeout", "5", fmt.Sprintf("http://[%s]:8080", dstIPv6)})
		o.Expect(err).NotTo(o.HaveOccurred(), "IPv6 connectivity test failed: %s", out)
		o.Expect(out).To(o.ContainSubstring("whereabouts-dualstack-test-pod"),
			"IPv6 connectivity: Expected response from hello-sdn server")
	})

	// OCP-69947: Macvlan pods send Unsolicited Neighbor Advertisements
	// Note: Marked as informing due to timing sensitivity with tcpdump in automated environment
	g.It("[JIRA:Networking][OTP] 69947-should send Unsolicited Neighbor Advertisements when macvlan pod is created", func() {
		// AWS Limitation: AWS VPC doesn't support L2 IPv6 multicast/NDP required for macvlan NAs.
		// This test validates ICMPv6 Neighbor Advertisement packets sent when macvlan pods are created,
		// which requires L2 network capabilities that AWS VPC blocks at the hypervisor level.
		// Test will SKIP on AWS and should PASS on bare metal, VMware, or other L2-capable platforms.
		// To verify this test actually works (not just skips), run on non-AWS infrastructure where
		// L2 multicast is supported. Check OTP CI results on bare metal clusters for validation.
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		o.Expect(err).NotTo(o.HaveOccurred())
		if len(nodes.Items) > 0 {
			providerID := nodes.Items[0].Spec.ProviderID
			if providerID != "" && (strings.Contains(providerID, "aws") || strings.Contains(providerID, "ec2")) {
				g.Skip("Skipping on AWS: AWS VPC doesn't support L2 IPv6 multicast/NDP required for macvlan Unsolicited Neighbor Advertisements")
			}
		}

		testNS := fmt.Sprintf("test-macvlan-na-69947-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NetworkAttachmentDefinition with dual-stack whereabouts IPAM")
		nadConfig := `{
			"cniVersion": "0.3.1",
			"name": "whereabouts-dualstack",
			"type": "macvlan",
			"mode": "bridge",
			"ipam": {
				"type": "whereabouts",
				"ipRanges": [
					{
						"range": "192.168.10.0/24"
					},
					{
						"range": "fd00:dead:beef:10::/64"
					}
				]
			}
		}`

		err = createNAD(ctx, config, testNS, "whereabouts-dualstack", nadConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating sniffer pod to capture ICMPv6 Neighbor Advertisements")
		snifferPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sniff-pod",
				Namespace: testNS,
				Labels: map[string]string{
					"app": "sniffer",
				},
				Annotations: map[string]string{
					"k8s.v1.cni.cncf.io/networks": "whereabouts-dualstack",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    "sniffer",
						Image:   imageHelloSDN,
						Command: []string{"/bin/sh", "-c"},
						Args: []string{
							// Start tcpdump to capture ICMPv6 Neighbor Advertisements on net1
							// Filter: icmp6 type 136 (Neighbor Advertisement)
							`tcpdump -i net1 -n 'icmp6 and icmp6[0] = 136' -w /tmp/capture.pcap &
							sleep 3600`,
						},
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"NET_RAW", "NET_ADMIN"},
							},
						},
					},
				},
			},
		}

		_, err = clientset.CoreV1().Pods(testNS).Create(ctx, snifferPod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		// Wait for sniffer pod to be running
		o.Eventually(func() corev1.PodPhase {
			pod, err := clientset.CoreV1().Pods(testNS).Get(ctx, "sniff-pod", metav1.GetOptions{})
			if err != nil {
				return corev1.PodPending
			}
			return pod.Status.Phase
		}, 60, 5).Should(o.Equal(corev1.PodRunning), "Sniffer pod should be running")

		// Wait for tcpdump to start capturing
		o.Eventually(func() bool {
			out, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
				[]string{"/bin/sh", "-c", "pgrep tcpdump >/dev/null 2>&1 && echo ready"})
			return err == nil && strings.Contains(out, "ready")
		}, 30, 2).Should(o.BeTrue(), "tcpdump should be running in sniffer pod")

		g.By("Creating 6 test pods with macvlan secondary network")
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: testNS,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(6),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "test-pod",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"name": "test-pod",
						},
						Annotations: map[string]string{
							"k8s.v1.cni.cncf.io/networks": "whereabouts-dualstack",
						},
					},
					Spec: corev1.PodSpec{
						Affinity: &corev1.Affinity{
							PodAffinity: &corev1.PodAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": "sniffer",
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name:  "test-pod",
								Image: imageHelloSDN,
								Env: []corev1.EnvVar{
									{
										Name:  "RESPONSE",
										Value: "Hello",
									},
								},
							},
						},
					},
				},
			},
		}

		_, err = clientset.AppsV1().Deployments(testNS).Create(ctx, deployment, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for all 6 test pods to reach Running state")
		o.Eventually(func() int {
			pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
				LabelSelector: "name=test-pod",
			})
			if err != nil {
				return 0
			}
			runningCount := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					runningCount++
				}
			}
			return runningCount
		}, 120, 10).Should(o.Equal(6), "All 6 test pods should be running")

		// Wait for Unsolicited Neighbor Advertisements to be captured
		o.Eventually(func() bool {
			out, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
				[]string{"/bin/sh", "-c", "tcpdump -r /tmp/capture.pcap -n 'icmp6 and icmp6[0] = 136' 2>/dev/null | grep -c 'Neighbor Advertisement'"})
			return err == nil && strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != ""
		}, 30, 5).Should(o.BeTrue(), "Should have captured at least one ICMPv6 Neighbor Advertisement")

		g.By("Analyzing captured ICMPv6 Neighbor Advertisements")
		// Kill tcpdump process (error ignored — pkill exits non-zero if already stopped)
		if _, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
			[]string{"/bin/sh", "-c", "pkill tcpdump"}); err != nil {
			g.GinkgoLogr.Error(err, "Failed to kill tcpdump process")
		}

		// Wait for tcpdump to stop and flush pcap file
		o.Eventually(func() bool {
			out, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
				[]string{"/bin/sh", "-c", "pgrep tcpdump >/dev/null 2>&1 && echo running || echo stopped"})
			return err == nil && strings.Contains(out, "stopped")
		}, 10, 1).Should(o.BeTrue(), "tcpdump should have stopped")

		// Read and analyze the pcap file using tcpdump
		// Check for ICMPv6 NA packets with solicited flag = 0 (Unsolicited)
		naOut, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
			[]string{"/bin/sh", "-c", `tcpdump -r /tmp/capture.pcap -n 'icmp6 and icmp6[0] = 136' -v 2>/dev/null | grep "Neighbor Advertisement" | wc -l`})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to analyze pcap: %s", naOut)

		naCount, err := strconv.Atoi(strings.TrimSpace(naOut))
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to parse NA count: %s", naOut)
		o.Expect(naCount).To(o.BeNumerically(">", 0), "Should have captured at least one ICMPv6 Neighbor Advertisement")

		g.By("Verifying Neighbor Advertisements are Unsolicited (solicited flag = 0)")
		// In unsolicited NA, the destination is ff02::1 (all nodes multicast)
		verifyOut, err := execInPod(ctx, clientset, config, testNS, "sniff-pod", "sniffer",
			[]string{"/bin/sh", "-c", `tcpdump -r /tmp/capture.pcap -n 'icmp6 and icmp6[0] = 136' 2>/dev/null | grep "ff02::1" | wc -l`})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to verify unsolicited NAs: %s", verifyOut)

		unsolicitedCount, err := strconv.Atoi(strings.TrimSpace(verifyOut))
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to parse unsolicited NA count: %s", verifyOut)
		o.Expect(unsolicitedCount).To(o.BeNumerically(">", 0),
			"Should have captured Unsolicited Neighbor Advertisements (destination ff02::1)")
	})

	// OCP-80524: Verify pods with isolated port using bridge-cni
	g.It("[JIRA:Networking][OTP] 80524-should isolate pods with portIsolation enabled on bridge CNI", func() {
		testNS := fmt.Sprintf("test-bridge-port-isolation-80524-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NetworkAttachmentDefinition with portIsolation enabled")
		nadConfig := `{
			"cniVersion": "0.4.0",
			"name": "bridge-isolated-ports",
			"type": "bridge",
			"portIsolation": true,
			"ipam": {
				"type": "host-local",
				"ranges": [
					[
						{
							"subnet": "192.168.10.0/24",
							"rangeStart": "192.168.10.1",
							"rangeEnd": "192.168.10.100"
						}
					],
					[
						{
							"subnet": "FD00:192:168:10::0/64",
							"rangeStart": "FD00:192:168:10::1",
							"rangeEnd": "FD00:192:168:10::100"
						}
					]
				]
			}
		}`

		err = createNAD(ctx, config, testNS, "bridge-isolated-ports", nadConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating Deployment with 2 pods on the same node")
		targetNode := getSchedulableNode(ctx, clientset)

		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "red-test-pod",
				Namespace: testNS,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(2),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "red",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"name": "red",
						},
						Annotations: map[string]string{
							"k8s.v1.cni.cncf.io/networks": "bridge-isolated-ports",
						},
					},
					Spec: corev1.PodSpec{
						NodeName: targetNode,
						Containers: []corev1.Container{
							{
								Name:  "red-test-pod",
								Image: imageHelloSDN,
								Ports: []corev1.ContainerPort{
									{ContainerPort: 8080},
									{ContainerPort: 443},
								},
								Env: []corev1.EnvVar{
									{
										Name:  "RESPONSE",
										Value: "red-test-pod",
									},
								},
							},
						},
					},
				},
			},
		}

		_, err = clientset.AppsV1().Deployments(testNS).Create(ctx, deployment, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for both pods to be Running")
		o.Eventually(func() int {
			pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
				LabelSelector: "name=red",
			})
			if err != nil {
				return 0
			}
			runningCount := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName == targetNode {
					runningCount++
				}
			}
			return runningCount
		}, 120, 5).Should(o.Equal(2), "Both pods should be running on the same node")

		g.By("Getting pod IPs from secondary network")
		pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
			LabelSelector: "name=red",
		})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(pods.Items)).To(o.Equal(2), "Should have exactly 2 pods")

		pod1 := pods.Items[0]
		pod2 := pods.Items[1]

		// Parse network status annotation to get secondary IP
		var pod1SecondaryIP, pod2SecondaryIP string
		if netStatus, ok := pod1.Annotations["k8s.v1.cni.cncf.io/network-status"]; ok {
			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())
			for _, net := range networks {
				if name, ok := net["name"].(string); ok && name == testNS+"/bridge-isolated-ports" {
					if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
						ip, ok := ips[0].(string)
						o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
						pod1SecondaryIP = ip
					}
				}
			}
		}
		o.Expect(pod1SecondaryIP).NotTo(o.BeEmpty(), "Pod1 should have secondary IP")

		if netStatus, ok := pod2.Annotations["k8s.v1.cni.cncf.io/network-status"]; ok {
			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())
			for _, net := range networks {
				if name, ok := net["name"].(string); ok && name == testNS+"/bridge-isolated-ports" {
					if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
						ip, ok := ips[0].(string)
						o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
						pod2SecondaryIP = ip
					}
				}
			}
		}
		o.Expect(pod2SecondaryIP).NotTo(o.BeEmpty(), "Pod2 should have secondary IP")

		g.By("Verifying pods cannot communicate via isolated bridge ports")
		// Ping should FAIL because ports are isolated
		pingOut, err := execInPod(ctx, clientset, config, testNS, pod1.Name, "red-test-pod",
			[]string{"ping", "-c", "3", "-W", "2", pod2SecondaryIP})
		o.Expect(err).To(o.HaveOccurred(), "Ping should fail between isolated ports")
		o.Expect(pingOut).To(o.Or(
			o.ContainSubstring("100% packet loss"),
			o.ContainSubstring("Network is unreachable"),
		), "Should show network isolation")
	})

	g.It("[JIRA:Networking][OTP] 80525-should allow communication on non-isolated network but not on isolated network", func() {
		testNS := fmt.Sprintf("test-bridge-mixed-isolation-80525-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Creating NAD with portIsolation enabled")
		nadIsolated := `{
			"cniVersion": "0.4.0",
			"name": "bridge-isolated-ports",
			"type": "bridge",
			"portIsolation": true,
			"ipam": {
				"type": "host-local",
				"ranges": [
					[
						{
							"subnet": "192.168.10.0/24",
							"rangeStart": "192.168.10.1",
							"rangeEnd": "192.168.10.100"
						}
					],
					[
						{
							"subnet": "FD00:192:168:10::0/64",
							"rangeStart": "FD00:192:168:10::1",
							"rangeEnd": "FD00:192:168:10::100"
						}
					]
				]
			}
		}`

		err = createNAD(ctx, config, testNS, "bridge-isolated-ports", nadIsolated)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating NAD with portIsolation disabled")
		nadNonIsolated := `{
			"cniVersion": "0.4.0",
			"name": "bridge-whereabouts",
			"portIsolation": false,
			"type": "bridge",
			"ipam": {
				"type": "whereabouts",
				"ipRanges": [
					{
						"range": "192.168.14.0/24",
						"rangeStart": "192.168.14.1",
						"rangeEnd": "192.168.14.100"
					},
					{
						"range": "FD00:192:168:14::0/64",
						"rangeStart": "FD00:192:168:14::1",
						"rangeEnd": "FD00:192:168:14::100"
					}
				]
			}
		}`

		err = createNAD(ctx, config, testNS, "bridge-whereabouts", nadNonIsolated)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating Deployment with 2 pods using both NADs on the same node")
		targetNode := getSchedulableNode(ctx, clientset)

		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "green-test-pod",
				Namespace: testNS,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(2),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "green",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"name": "green",
						},
						Annotations: map[string]string{
							"k8s.v1.cni.cncf.io/networks": "bridge-isolated-ports, bridge-whereabouts",
						},
					},
					Spec: corev1.PodSpec{
						NodeName: targetNode,
						Containers: []corev1.Container{
							{
								Name:  "green-test-pod",
								Image: imageHelloSDN,
								Ports: []corev1.ContainerPort{
									{ContainerPort: 8080},
									{ContainerPort: 443},
								},
								Env: []corev1.EnvVar{
									{
										Name:  "RESPONSE",
										Value: "green-test-pod",
									},
								},
							},
						},
					},
				},
			},
		}

		_, err = clientset.AppsV1().Deployments(testNS).Create(ctx, deployment, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for both pods to be Running")
		o.Eventually(func() int {
			pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
				LabelSelector: "name=green",
			})
			if err != nil {
				return 0
			}
			runningCount := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName == targetNode {
					runningCount++
				}
			}
			return runningCount
		}, 120, 5).Should(o.Equal(2), "Both pods should be running on the same node")

		g.By("Getting pod IPs from both networks")
		pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
			LabelSelector: "name=green",
		})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(pods.Items)).To(o.Equal(2), "Should have exactly 2 pods")

		pod1 := pods.Items[0]
		pod2 := pods.Items[1]

		// Parse network status to get IPs from both networks
		var pod1IsolatedIP, pod1NonIsolatedIP, pod2IsolatedIP, pod2NonIsolatedIP string

		if netStatus, ok := pod1.Annotations["k8s.v1.cni.cncf.io/network-status"]; ok {
			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())
			for _, net := range networks {
				if name, ok := net["name"].(string); ok {
					if name == testNS+"/bridge-isolated-ports" {
						if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							pod1IsolatedIP = ip
						}
					} else if name == testNS+"/bridge-whereabouts" {
						if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							pod1NonIsolatedIP = ip
						}
					}
				}
			}
		}
		o.Expect(pod1IsolatedIP).NotTo(o.BeEmpty(), "Pod1 should have isolated network IP")
		o.Expect(pod1NonIsolatedIP).NotTo(o.BeEmpty(), "Pod1 should have non-isolated network IP")

		if netStatus, ok := pod2.Annotations["k8s.v1.cni.cncf.io/network-status"]; ok {
			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())
			for _, net := range networks {
				if name, ok := net["name"].(string); ok {
					if name == testNS+"/bridge-isolated-ports" {
						if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							pod2IsolatedIP = ip
						}
					} else if name == testNS+"/bridge-whereabouts" {
						if ips, ok := net["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							pod2NonIsolatedIP = ip
						}
					}
				}
			}
		}
		o.Expect(pod2IsolatedIP).NotTo(o.BeEmpty(), "Pod2 should have isolated network IP")
		o.Expect(pod2NonIsolatedIP).NotTo(o.BeEmpty(), "Pod2 should have non-isolated network IP")

		g.By("Verifying pods CANNOT communicate via isolated network")
		// Ping should FAIL on isolated network
		isolatedOut, err := execInPod(ctx, clientset, config, testNS, pod1.Name, "green-test-pod",
			[]string{"ping", "-c", "3", "-W", "2", pod2IsolatedIP})
		o.Expect(err).To(o.HaveOccurred(), "Ping should fail on isolated network")
		o.Expect(isolatedOut).To(o.Or(
			o.ContainSubstring("100% packet loss"),
			o.ContainSubstring("Network is unreachable"),
		), "Should show network isolation on isolated network")

		g.By("Verifying pods CAN communicate via non-isolated network")
		// Ping should SUCCEED on non-isolated network
		nonIsolatedOut, err := execInPod(ctx, clientset, config, testNS, pod1.Name, "green-test-pod",
			[]string{"ping", "-c", "3", "-W", "2", pod2NonIsolatedIP})
		o.Expect(err).NotTo(o.HaveOccurred(), "Ping should succeed on non-isolated network")
		o.Expect(nonIsolatedOut).To(o.ContainSubstring("0% packet loss"), "Should show successful ping on non-isolated network")
	})

	g.It("[JIRA:Networking][OTP] 77102-should have secure permissions on CNI configuration files", func() {
		testNS := fmt.Sprintf("test-cni-permissions-77102-%s", randomSuffix())

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce":         "privileged",
					"pod-security.kubernetes.io/audit":           "privileged",
					"pod-security.kubernetes.io/warn":            "privileged",
					"security.openshift.io/scc.podSecurityLabelSync": "false",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			g.By("Cleaning up test namespace")
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		g.By("Checking multus config permissions via multus pods")
		multusPods, err := clientset.CoreV1().Pods("openshift-multus").List(ctx, metav1.ListOptions{
			LabelSelector: "app=multus",
		})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(multusPods.Items)).To(o.BeNumerically(">", 0), "Expected at least one multus pod")

		// Check first multus pod for config file permissions
		multusPod := multusPods.Items[0].Name
		output, err := execInPod(ctx, clientset, config, "openshift-multus", multusPod, "kube-multus",
			[]string{"/bin/bash", "-c", "stat -c '%a %n' /host/etc/cni/net.d/*.conf"})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to check multus config permissions")

		g.By("Verifying multus config has 600 permissions")
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			o.Expect(len(parts)).To(o.BeNumerically(">=", 2), "Invalid stat output: %s", line)
			perms := parts[0]
			filename := parts[1]
			o.Expect(perms).To(o.Equal("600"),
				"CIS violation: %s has insecure permissions %s (expected 600)", filename, perms)
		}

		g.By("Checking whereabouts config permissions")
		nodeName := getSchedulableNode(ctx, clientset)

		// Create debug pod on node
		debugPodName := "cis-perms-check-77102"
		debugPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      debugPodName,
				Namespace: testNS,
			},
			Spec: corev1.PodSpec{
				NodeName:    nodeName,
				HostNetwork: true,
				HostPID:     true,
				Containers: []corev1.Container{
					{
						Name:    "debug",
						Image:   imageUBIMin,
						Command: []string{"sleep", "300"},
						SecurityContext: &corev1.SecurityContext{
							Privileged: boolPtr(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "host",
								MountPath: "/host",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "host",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/",
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		}

		_, err = clientset.CoreV1().Pods(testNS).Create(ctx, debugPod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		// Wait for debug pod to be running
		o.Eventually(func() corev1.PodPhase {
			p, err := clientset.CoreV1().Pods(testNS).Get(ctx, debugPodName, metav1.GetOptions{})
			if err != nil {
				return corev1.PodPending
			}
			return p.Status.Phase
		}, 60, 5).Should(o.Equal(corev1.PodRunning), "Debug pod did not reach Running state")

		// Check whereabouts config file permissions
		output, err = execInPod(ctx, clientset, config, testNS, debugPodName, "debug",
			[]string{"/bin/bash", "-c", "stat -c '%a %n' /host/etc/kubernetes/cni/net.d/whereabouts.d/*.conf /host/etc/kubernetes/cni/net.d/whereabouts.d/*.kubeconfig 2>/dev/null || true"})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to check whereabouts config permissions")

		g.By("Verifying whereabouts configs have 600 permissions")
		if strings.TrimSpace(output) != "" {
			lines = strings.Split(strings.TrimSpace(output), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.Fields(line)
				o.Expect(len(parts)).To(o.BeNumerically(">=", 2), "Invalid stat output: %s", line)
				perms := parts[0]
				filename := parts[1]
				o.Expect(perms).To(o.Equal("600"),
					"CIS violation: %s has insecure permissions %s (expected 600)", filename, perms)
			}
		}
	})

	g.It("[JIRA:Networking][OTP][Serial][Disruptive] 74933-should reconcile whereabouts IPs after forced node reboot", g.Label("Serial"), func() {
		// NOTE: This is a disruptive test that force reboots a node
		// It runs in serial CI jobs (e.g., e2e-aws-ovn-serial) designed for such tests
		// Related: OCPBUGS-35923, OCPBUGS-16008
		testNS := fmt.Sprintf("test-whereabouts-reconcile-74933-%s", randomSuffix())
		nadName := "whereabouts-reconcile"
		statefulSetName := "test-sts"
		replicas := int32(2)

		g.By("Creating test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "privileged",
					"pod-security.kubernetes.io/audit":   "privileged",
					"pod-security.kubernetes.io/warn":    "privileged",
				},
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			g.By("Cleaning up test namespace")
			if err := clientset.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete test namespace", "namespace", testNS)
			}
		}()

		// Get schedulable nodes
		g.By("Finding schedulable nodes")
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(nodes.Items)).To(o.BeNumerically(">", 0), "Should have at least one node")

		var targetNode string
		var fallbackNode string
		for _, node := range nodes.Items {
			isReady := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					isReady = true
					break
				}
			}
			if !isReady || node.Spec.Unschedulable {
				continue
			}
			_, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]
			_, isMaster := node.Labels["node-role.kubernetes.io/master"]
			if isControlPlane || isMaster {
				if fallbackNode == "" {
					fallbackNode = node.Name
				}
				continue
			}
			targetNode = node.Name
			break
		}
		if targetNode == "" {
			targetNode = fallbackNode
		}
		o.Expect(targetNode).NotTo(o.BeEmpty(), "Should have at least one Ready, schedulable node")

		g.By(fmt.Sprintf("Updating whereabouts reconciler configuration on node %s", targetNode))
		// Create debug pod on target node to modify whereabouts config
		debugPodName := "whereabouts-config-update"
		debugPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      debugPodName,
				Namespace: testNS,
			},
			Spec: corev1.PodSpec{
				NodeName:    targetNode,
				HostNetwork: true,
				HostPID:     true,
				Containers: []corev1.Container{
					{
						Name:    "debug",
						Image:   imageUBIMin,
						Command: []string{"sleep", "600"},
						SecurityContext: &corev1.SecurityContext{
							Privileged: boolPtr(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "host",
								MountPath: "/host",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "host",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/",
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		}

		_, err = clientset.CoreV1().Pods(testNS).Create(ctx, debugPod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		// Wait for debug pod to be running
		o.Eventually(func() corev1.PodPhase {
			p, err := clientset.CoreV1().Pods(testNS).Get(ctx, debugPodName, metav1.GetOptions{})
			if err != nil {
				return corev1.PodPending
			}
			return p.Status.Phase
		}, 60, 5).Should(o.Equal(corev1.PodRunning), "Debug pod did not reach Running state")

		// Update whereabouts reconciler configuration
		updateCmd := []string{
			"/bin/bash", "-c",
			`
			# Backup original config
			cp /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf.backup 2>/dev/null || true

			# Update reconciler_cron_expression
			if [ -f /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf ]; then
				sed -i 's/"reconciler_cron_expression".*/"reconciler_cron_expression": "*\/1 * * * *",/' /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf
				echo "Updated whereabouts reconciler config"
			else
				echo "whereabouts.conf not found"
				exit 1
			fi
			`,
		}

		output, err := execInPod(ctx, clientset, config, testNS, debugPodName, "debug", updateCmd)
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to update whereabouts config: %s", output)
		o.Expect(output).To(o.ContainSubstring("Updated whereabouts reconciler config"))

		g.By("Creating NetworkAttachmentDefinition with whereabouts IPAM")
		// IP range sized for exactly the number of replicas (tight range for reconciliation test)
		nadConfig := fmt.Sprintf(`{
			"cniVersion": "0.3.1",
			"name": "%s",
			"type": "bridge",
			"bridge": "wb-test-br",
			"ipam": {
				"type": "whereabouts",
				"range": "192.168.50.0/30",
				"reconciler_cron_expression": "*/1 * * * *"
			}
		}`, nadName)

		err = createNAD(ctx, config, testNS, nadName, nadConfig)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating StatefulSet with pods using whereabouts")
		statefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statefulSetName,
				Namespace: testNS,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(replicas),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": statefulSetName,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": statefulSetName,
						},
						Annotations: map[string]string{
							"k8s.v1.cni.cncf.io/networks": nadName,
						},
					},
					Spec: corev1.PodSpec{
						NodeName: targetNode, // Pin to same node
						Containers: []corev1.Container{
							{
								Name:    "test",
								Image:   imageUBIMin,
								Command: []string{"sleep", "3600"},
							},
						},
					},
				},
			},
		}

		_, err = clientset.AppsV1().StatefulSets(testNS).Create(ctx, statefulSet, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Waiting for StatefulSet pods to be Running")
		o.Eventually(func() bool {
			sts, err := clientset.AppsV1().StatefulSets(testNS).Get(ctx, statefulSetName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			return sts.Status.ReadyReplicas == replicas
		}, 120, 5).Should(o.BeTrue(), "StatefulSet pods did not reach Running state")

		g.By("Recording IP addresses from whereabouts before node reboot")
		podIPs := make(map[string]string)
		for i := int32(0); i < replicas; i++ {
			podName := fmt.Sprintf("%s-%d", statefulSetName, i)
			pod, err := clientset.CoreV1().Pods(testNS).Get(ctx, podName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())

			// Parse network status annotation
			netStatus := pod.Annotations["k8s.v1.cni.cncf.io/network-status"]
			o.Expect(netStatus).NotTo(o.BeEmpty(), "Pod %s has no network-status annotation", podName)

			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())

			// Find the whereabouts network IP
			// Network name in status can be "nadName" or "namespace/nadName"
			var secondaryIP string
			for _, network := range networks {
				if name, ok := network["name"].(string); ok {
					if name == nadName || name == fmt.Sprintf("%s/%s", testNS, nadName) {
						if ips, ok := network["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							secondaryIP = ip
							break
						}
					}
				}
			}
			o.Expect(secondaryIP).NotTo(o.BeEmpty(), "Pod %s has no secondary IP from whereabouts", podName)
			podIPs[podName] = secondaryIP
			g.By(fmt.Sprintf("Pod %s has secondary IP: %s", podName, secondaryIP))
		}

		g.By(fmt.Sprintf("Force rebooting node %s", targetNode))
		rebootCmd := []string{
			"/bin/bash", "-c",
			"chroot /host systemctl reboot --force",
		}

		_, _ = execInPod(ctx, clientset, config, testNS, debugPodName, "debug", rebootCmd)

		g.By("Waiting for node to become NotReady")
		o.Eventually(func() bool {
			node, err := clientset.CoreV1().Nodes().Get(ctx, targetNode, metav1.GetOptions{})
			if err != nil {
				return false
			}
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, 120, 5).Should(o.BeTrue(), "Node did not become NotReady after reboot command")

		g.By("Waiting for node to come back and become Ready")
		o.Eventually(func() bool {
			node, err := clientset.CoreV1().Nodes().Get(ctx, targetNode, metav1.GetOptions{})
			if err != nil {
				return false
			}
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, 600, 10).Should(o.BeTrue(), "Node did not come back Ready after reboot")

		g.By("Force deleting StatefulSet pods to trigger recreation")
		gracePeriod := int64(0)
		for i := int32(0); i < replicas; i++ {
			podName := fmt.Sprintf("%s-%d", statefulSetName, i)
			err := clientset.CoreV1().Pods(testNS).Delete(ctx, podName, metav1.DeleteOptions{
				GracePeriodSeconds: &gracePeriod,
			})
			if err != nil && !apierrors.IsNotFound(err) {
				g.GinkgoLogr.Error(err, "Failed to delete pod", "pod", podName)
			}
		}

		g.By("Waiting for StatefulSet pods to be recreated and Running")
		o.Eventually(func() bool {
			sts, err := clientset.AppsV1().StatefulSets(testNS).Get(ctx, statefulSetName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			return sts.Status.ReadyReplicas == replicas
		}, 300, 10).Should(o.BeTrue(), "StatefulSet pods did not get recreated")

		g.By("Verifying pods get the same IPs after reboot (whereabouts reconciliation)")
		for i := int32(0); i < replicas; i++ {
			podName := fmt.Sprintf("%s-%d", statefulSetName, i)
			originalIP := podIPs[podName]

			pod, err := clientset.CoreV1().Pods(testNS).Get(ctx, podName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())

			// Parse network status annotation
			netStatus := pod.Annotations["k8s.v1.cni.cncf.io/network-status"]
			o.Expect(netStatus).NotTo(o.BeEmpty(), "Pod %s has no network-status annotation after reboot", podName)

			var networks []map[string]interface{}
			err = json.Unmarshal([]byte(netStatus), &networks)
			o.Expect(err).NotTo(o.HaveOccurred())

			// Find the whereabouts network IP
			// Network name in status can be "nadName" or "namespace/nadName"
			var newIP string
			for _, network := range networks {
				if name, ok := network["name"].(string); ok {
					if name == nadName || name == fmt.Sprintf("%s/%s", testNS, nadName) {
						if ips, ok := network["ips"].([]interface{}); ok && len(ips) > 0 {
							ip, ok := ips[0].(string)
							o.Expect(ok).To(o.BeTrue(), "expected IP to be a string, got %T", ips[0])
							newIP = ip
							break
						}
					}
				}
			}
			o.Expect(newIP).NotTo(o.BeEmpty(), "Pod %s has no secondary IP after reboot", podName)
			o.Expect(newIP).To(o.Equal(originalIP),
				"Pod %s IP changed after reboot: was %s, now %s (whereabouts reconciliation failed)",
				podName, originalIP, newIP)
			g.By(fmt.Sprintf("Pod %s kept the same IP: %s", podName, newIP))
		}

		g.By("Restoring original whereabouts configuration")
		restoreCmd := []string{
			"/bin/bash", "-c",
			"mv /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf.backup /host/etc/kubernetes/cni/net.d/whereabouts.d/whereabouts.conf 2>/dev/null || true",
		}
		// Best effort restore - may fail if debug pod was killed during reboot
		_, _ = execInPod(ctx, clientset, config, testNS, debugPodName, "debug", restoreCmd)
	})
})

// createNAD creates a NetworkAttachmentDefinition
func createNAD(ctx context.Context, config *rest.Config, namespace, name, nadConfig string) error {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	nadGVR := schema.GroupVersionResource{
		Group:    "k8s.cni.cncf.io",
		Version:  "v1",
		Resource: "network-attachment-definitions",
	}

	nad := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.cni.cncf.io/v1",
			"kind":       "NetworkAttachmentDefinition",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"config": nadConfig,
			},
		},
	}

	_, err = dynamicClient.Resource(nadGVR).Namespace(namespace).Create(ctx, nad, metav1.CreateOptions{})
	return err
}

// Helper functions

func getSchedulableNode(ctx context.Context, clientset kubernetes.Interface) string {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(len(nodes.Items)).To(o.BeNumerically(">", 0), "Should have at least one node")

	var nodeName string
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				if !node.Spec.Unschedulable {
					nodeName = node.Name
					break
				}
			}
		}
		if nodeName != "" {
			break
		}
	}
	o.Expect(nodeName).NotTo(o.BeEmpty(), "Should have at least one Ready, schedulable node")
	return nodeName
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// int32Ptr returns a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}

// boolPtr returns a pointer to a bool
func boolPtr(b bool) *bool {
	return &b
}

// execInPod executes a command in a pod and returns the output
func execInPod(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName, containerName string, command []string) (string, error) {

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return "", err
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, runtime.NewParameterCodec(scheme))

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String() + "\n" + stderr.String(), err
	}

	return stdout.String(), nil
}
