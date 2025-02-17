package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common/consts"

	"github.com/keyval-dev/odigos/cli/cmd/resources"
	"github.com/keyval-dev/odigos/cli/pkg/kube"
	"github.com/keyval-dev/odigos/cli/pkg/log"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	odigosCloudApiKeyFlag    string
	namespaceFlag            string
	versionFlag              string
	skipWait                 bool
	telemetryEnabled         bool
	sidecarInstrumentation   bool
	psp                      bool
	ignoredNamespaces        []string
	DefaultIgnoredNamespaces = []string{"odigos-system", "kube-system", "local-path-storage", "istio-system", "linkerd"}

	instrumentorImage string
	odigletImage      string
	autoScalerImage   string
	imagePrefix       string
)

type ResourceCreationFunc func(ctx context.Context, cmd *cobra.Command, client *kube.Client, ns string) error

// installCmd represents the install command
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Odigos",
	Long: `Install Odigos in your kubernetes cluster. 
This command will install k8s components that will auto-instrument your applications with OpenTelemetry and send traces, metrics and logs to any telemetry backend`,
	Run: func(cmd *cobra.Command, args []string) {

		client, err := kube.CreateClient(cmd)
		if err != nil {
			kube.PrintClientErrorAndExit(err)
		}
		ctx := cmd.Context()
		ns := cmd.Flag("namespace").Value.String()
		cmd.Flags().StringSliceVar(&ignoredNamespaces, "ignore-namespace", DefaultIgnoredNamespaces, "--ignore-namespace foo logging")

		// check if odigos is already installed
		existingOdigosNs, err := resources.GetOdigosNamespace(client, ctx)
		if err == nil {
			fmt.Printf("\033[31mERROR\033[0m Odigos is already installed in namespace \"%s\". If you wish to re-install, run \"odigos uninstall\" first.\n", existingOdigosNs)
			os.Exit(1)
		} else if !resources.IsErrNoOdigosNamespaceFound(err) {
			fmt.Printf("\033[31mERROR\033[0m Failed to check if Odigos is already installed: %s\n", err)
			os.Exit(1)
		}

		config := createOdigosConfigSpec()

		isOdigosCloud := odigosCloudApiKeyFlag != ""
		if isOdigosCloud {
			err = verifyOdigosCloudApiKey(odigosCloudApiKeyFlag)
			if err != nil {
				fmt.Println("Odigos install failed - invalid api-key format.")
				os.Exit(1)
			}
		}

		fmt.Printf("Installing Odigos version %s in namespace %s ...\n", versionFlag, ns)

		// namespace is created on "install" and is not managed by resource manager
		createKubeResourceWithLogging(ctx, fmt.Sprintf("Creating namespace %s", ns),
			client, cmd, ns, createNamespace)

		resourceManagers := resources.CreateResourceManagers(client, ns, isOdigosCloud, &odigosCloudApiKeyFlag, &config)
		err = resources.ApplyResourceManagers(ctx, client, resourceManagers, "Creating")
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Failed to install Odigos: %s\n", err)
			os.Exit(1)
		}

		if !skipWait {
			l := log.Print("Waiting for Odigos pods to be ready ...")
			err := wait.PollImmediate(1*time.Second, 3*time.Minute, arePodsReady(ctx, client, ns))
			if err != nil {
				l.Error(err)
			}

			l.Success()
		}

		fmt.Printf("\n\u001B[32mSUCCESS:\u001B[0m Odigos installed.\n")
	},
}

func arePodsReady(ctx context.Context, client *kube.Client, ns string) func() (bool, error) {
	return func() (bool, error) {
		pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		runningPods := 0
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodFailed {
				return false, fmt.Errorf("pod %s failed", p.Name)
			}

			if p.Status.Phase == corev1.PodRunning {
				runningPods++
			}
		}

		return runningPods == len(pods.Items), nil
	}
}

func createNamespace(ctx context.Context, cmd *cobra.Command, client *kube.Client, ns string) error {
	_, err := client.CoreV1().Namespaces().Create(ctx, resources.NewNamespace(ns), metav1.CreateOptions{})
	return err
}

func createOdigosConfigSpec() odigosv1.OdigosConfigurationSpec {
	return odigosv1.OdigosConfigurationSpec{
		OdigosVersion:          versionFlag,
		ConfigVersion:          1, // config version starts at 1 and incremented on every config change
		TelemetryEnabled:       telemetryEnabled,
		SidecarInstrumentation: sidecarInstrumentation,
		IgnoredNamespaces:      ignoredNamespaces,
		Psp:                    psp,
		ImagePrefix:            imagePrefix,
		OdigletImage:           odigletImage,
		InstrumentorImage:      instrumentorImage,
		AutoscalerImage:        autoScalerImage,
	}
}

func createKubeResourceWithLogging(ctx context.Context, msg string, client *kube.Client, cmd *cobra.Command, ns string, create ResourceCreationFunc) {
	l := log.Print(msg)
	err := create(ctx, cmd, client, ns)
	if err != nil {
		l.Error(err)
	}

	l.Success()
}

func init() {
	rootCmd.AddCommand(installCmd)
	installCmd.Flags().StringVarP(&namespaceFlag, "namespace", "n", consts.DefaultNamespace, "target k8s namespace for Odigos installation")
	installCmd.Flags().StringVarP(&odigosCloudApiKeyFlag, "api-key", "k", "", "api key for odigos cloud")
	installCmd.Flags().BoolVar(&skipWait, "nowait", false, "skip waiting for odigos pods to be ready")
	installCmd.Flags().BoolVar(&telemetryEnabled, "telemetry", true, "send general telemetry regarding Odigos usage")
	installCmd.Flags().BoolVar(&sidecarInstrumentation, "sidecar-instrumentation", false, "use sidecars for eBPF instrumentations")
	installCmd.Flags().StringVar(&odigletImage, "odiglet-image", "keyval/odigos-odiglet", "odiglet container image name")
	installCmd.Flags().StringVar(&instrumentorImage, "instrumentor-image", "keyval/odigos-instrumentor", "instrumentor container image name")
	installCmd.Flags().StringVar(&autoScalerImage, "autoscaler-image", "keyval/odigos-autoscaler", "autoscaler container image name")
	installCmd.Flags().StringVar(&imagePrefix, "image-prefix", "", "prefix for all container images. used when your cluster doesn't have access to docker hub")
	installCmd.Flags().BoolVar(&psp, "psp", false, "enable pod security policy")

	if OdigosVersion != "" {
		versionFlag = OdigosVersion
	} else {
		installCmd.Flags().StringVar(&versionFlag, "version", OdigosVersion, "for development purposes only")
	}
}
