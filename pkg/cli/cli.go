package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	kubecmd "k8s.io/kubectl/pkg/cmd"
	"k8s.io/kubectl/pkg/cmd/plugin"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/completion"
	ktemplates "k8s.io/kubectl/pkg/util/templates"
	kterm "k8s.io/kubectl/pkg/util/term"

	"github.com/openshift/oc/pkg/cli/admin"
	"github.com/openshift/oc/pkg/cli/cancelbuild"
	"github.com/openshift/oc/pkg/cli/debug"
	"github.com/openshift/oc/pkg/cli/deployer"
	"github.com/openshift/oc/pkg/cli/expose"
	"github.com/openshift/oc/pkg/cli/extract"
	"github.com/openshift/oc/pkg/cli/idle"
	"github.com/openshift/oc/pkg/cli/image"
	"github.com/openshift/oc/pkg/cli/importimage"
	"github.com/openshift/oc/pkg/cli/kubectlwrappers"
	"github.com/openshift/oc/pkg/cli/login"
	"github.com/openshift/oc/pkg/cli/logout"
	"github.com/openshift/oc/pkg/cli/logs"
	"github.com/openshift/oc/pkg/cli/newapp"
	"github.com/openshift/oc/pkg/cli/newbuild"
	"github.com/openshift/oc/pkg/cli/observe"
	"github.com/openshift/oc/pkg/cli/options"
	"github.com/openshift/oc/pkg/cli/policy"
	"github.com/openshift/oc/pkg/cli/process"
	"github.com/openshift/oc/pkg/cli/project"
	"github.com/openshift/oc/pkg/cli/projects"
	"github.com/openshift/oc/pkg/cli/recycle"
	"github.com/openshift/oc/pkg/cli/registry"
	"github.com/openshift/oc/pkg/cli/requestproject"
	"github.com/openshift/oc/pkg/cli/rollback"
	"github.com/openshift/oc/pkg/cli/rollout"
	"github.com/openshift/oc/pkg/cli/rsh"
	"github.com/openshift/oc/pkg/cli/rsync"
	"github.com/openshift/oc/pkg/cli/secrets"
	"github.com/openshift/oc/pkg/cli/serviceaccounts"
	"github.com/openshift/oc/pkg/cli/set"
	"github.com/openshift/oc/pkg/cli/startbuild"
	"github.com/openshift/oc/pkg/cli/status"
	"github.com/openshift/oc/pkg/cli/tag"
	"github.com/openshift/oc/pkg/cli/version"
	"github.com/openshift/oc/pkg/cli/whoami"
)

const productName = `OpenShift`

var (
	cliLong = heredoc.Doc(`
    ` + productName + ` Client

    This client helps you develop, build, deploy, and run your applications on any
    OpenShift or Kubernetes cluster. It also includes the administrative
    commands for managing a cluster under the 'adm' subcommand.`)

	cliExplain = heredoc.Doc(`
    To familiarize yourself with OpenShift, login to your cluster and try creating a sample application:

        oc login mycluster.mycompany.com
        oc new-project my-example
        oc new-app django-psql-example
        oc logs -f bc/django-psql-example

    To see what has been created, run:

        oc status

    and get a command shell inside one of the created containers with:

        oc rsh dc/postgresql

    To see the list of available toolchains for building applications, run:

        oc new-app -L

    Since OpenShift runs on top of Kubernetes, your favorite kubectl commands are also present in oc,
    allowing you to quickly switch between development and debugging. You can also run kubectl directly
    against any OpenShift cluster using the kubeconfig file created by 'oc login'.

    For more on OpenShift, see the documentation at https://docs.openshift.com.

    To see the full list of commands supported, run 'oc --help'.`)
)

func NewDefaultOcCommand(in io.Reader, out, errout io.Writer) *cobra.Command {
	cmd := NewOcCommand(in, out, errout)

	if len(os.Args) <= 1 {
		return cmd
	}

	cmdPathPieces := os.Args[1:]
	pluginHandler := kubecmd.NewDefaultPluginHandler(plugin.ValidPluginFilenamePrefixes)

	// only look for suitable extension executables if
	// the specified command does not already exist
	if _, _, err := cmd.Find(cmdPathPieces); err != nil {
		// Also check the commands that will be added by Cobra.
		// These commands are only added once rootCmd.Execute() is called, so we
		// need to check them explicitly here.
		var cmdName string // first "non-flag" arguments
		for _, arg := range cmdPathPieces {
			if !strings.HasPrefix(arg, "-") {
				cmdName = arg
				break
			}
		}

		switch cmdName {
		case "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			// Don't search for a plugin
		default:
			if err := kubecmd.HandlePluginCommand(pluginHandler, cmdPathPieces); err != nil {
				fmt.Fprintf(errout, "%v\n", err)
				os.Exit(1)
			}
		}
	}

	return cmd
}

func NewOcCommand(in io.Reader, out, err io.Writer) *cobra.Command {
	warningHandler := rest.NewWarningWriter(err, rest.WarningWriterOptions{Deduplicate: true, Color: kterm.AllowsColorOutput(err)})
	warningsAsErrors := false
	// Main command
	cmds := &cobra.Command{
		Use:   "oc",
		Short: "Command line tools for managing applications",
		Long:  cliLong,
		Run:   runHelp,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			rest.SetDefaultWarningHandler(warningHandler)
			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if warningsAsErrors {
				count := warningHandler.WarningCount()
				switch count {
				case 0:
					// no warnings
				case 1:
					return fmt.Errorf("%d warning received", count)
				default:
					return fmt.Errorf("%d warnings received", count)
				}
			}
			return nil
		},
	}

	flags := cmds.PersistentFlags()
	flags.BoolVar(&warningsAsErrors, "warnings-as-errors", warningsAsErrors, "Treat warnings received from the server as errors and exit with a non-zero exit code")

	kubeConfigFlags := genericclioptions.NewConfigFlags(true).WithDiscoveryBurst(350).WithDiscoveryQPS(50.0)
	kubeConfigFlags.AddFlags(flags)
	matchVersionKubeConfigFlags := kcmdutil.NewMatchVersionFlags(kubeConfigFlags)
	matchVersionKubeConfigFlags.AddFlags(cmds.PersistentFlags())
	cmds.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	f := kcmdutil.NewFactory(matchVersionKubeConfigFlags)

	ioStreams := genericclioptions.IOStreams{In: in, Out: out, ErrOut: err}

	loginCmd := login.NewCmdLogin(f, ioStreams)
	secretcmds := secrets.NewCmdSecrets(f, ioStreams)

	groups := ktemplates.CommandGroups{
		{
			Message: "Basic Commands:",
			Commands: []*cobra.Command{
				loginCmd,
				requestproject.NewCmdRequestProject(f, ioStreams),
				newapp.NewCmdNewApplication(f, ioStreams),
				status.NewCmdStatus(f, ioStreams),
				project.NewCmdProject(f, ioStreams),
				projects.NewCmdProjects(f, ioStreams),
				kubectlwrappers.NewCmdExplain(f, ioStreams),
			},
		},
		{
			Message: "Build and Deploy Commands:",
			Commands: []*cobra.Command{
				rollout.NewCmdRollout(f, ioStreams),
				rollback.NewCmdRollback(f, ioStreams),
				newbuild.NewCmdNewBuild(f, ioStreams),
				startbuild.NewCmdStartBuild(f, ioStreams),
				cancelbuild.NewCmdCancelBuild(f, ioStreams),
				importimage.NewCmdImportImage(f, ioStreams),
				tag.NewCmdTag(f, ioStreams),
			},
		},
		{
			Message: "Application Management Commands:",
			Commands: []*cobra.Command{
				kubectlwrappers.NewCmdCreate(f, ioStreams),
				kubectlwrappers.NewCmdApply(f, ioStreams),
				kubectlwrappers.NewCmdGet(f, ioStreams),
				kubectlwrappers.NewCmdDescribe(f, ioStreams),
				kubectlwrappers.NewCmdEdit(f, ioStreams),
				set.NewCmdSet(f, ioStreams),
				kubectlwrappers.NewCmdLabel(f, ioStreams),
				kubectlwrappers.NewCmdAnnotate(f, ioStreams),
				expose.NewCmdExpose(f, ioStreams),
				kubectlwrappers.NewCmdDelete(f, ioStreams),
				kubectlwrappers.NewCmdScale(f, ioStreams),
				kubectlwrappers.NewCmdAutoscale(f, ioStreams),
				secretcmds,
				serviceaccounts.NewCmdServiceAccounts(f, ioStreams),
			},
		},
		{
			Message: "Troubleshooting and Debugging Commands:",
			Commands: []*cobra.Command{
				logs.NewCmdLogs(f, ioStreams),
				rsh.NewCmdRsh(f, ioStreams),
				rsync.NewCmdRsync(f, ioStreams),
				kubectlwrappers.NewCmdPortForward(f, ioStreams),
				debug.NewCmdDebug(f, ioStreams),
				kubectlwrappers.NewCmdExec(f, ioStreams),
				kubectlwrappers.NewCmdProxy(f, ioStreams),
				kubectlwrappers.NewCmdAttach(f, ioStreams),
				kubectlwrappers.NewCmdRun(f, ioStreams),
				kubectlwrappers.NewCmdCp(f, ioStreams),
				kubectlwrappers.NewCmdWait(f, ioStreams),
			},
		},
		{
			Message: "Advanced Commands:",
			Commands: []*cobra.Command{
				admin.NewCommandAdmin(f, ioStreams),
				kubectlwrappers.NewCmdReplace(f, ioStreams),
				kubectlwrappers.NewCmdPatch(f, ioStreams),
				process.NewCmdProcess(f, ioStreams),
				extract.NewCmdExtract(f, ioStreams),
				observe.NewCmdObserve(f, ioStreams),
				policy.NewCmdPolicy(f, ioStreams),
				kubectlwrappers.NewCmdAuth(f, ioStreams),
				image.NewCmdImage(f, ioStreams),
				registry.NewCmd(f, ioStreams),
				idle.NewCmdIdle(f, ioStreams),
				kubectlwrappers.NewCmdApiVersions(f, ioStreams),
				kubectlwrappers.NewCmdApiResources(f, ioStreams),
				kubectlwrappers.NewCmdClusterInfo(f, ioStreams),
				kubectlwrappers.NewCmdDiff(f, ioStreams),
				kubectlwrappers.NewCmdKustomize(ioStreams),
			},
		},
		{
			Message: "Settings Commands:",
			Commands: []*cobra.Command{
				logout.NewCmdLogout(f, ioStreams),
				kubectlwrappers.NewCmdConfig(f, ioStreams),
				whoami.NewCmdWhoAmI(f, ioStreams),
				kubectlwrappers.NewCmdCompletion(ioStreams),
			},
		},
	}
	groups.Add(cmds)

	filters := []string{"options"}

	changeSharedFlagDefaults(cmds)

	ktemplates.ActsAsRootCommand(cmds, filters, groups...).
		ExposeFlags(loginCmd, "certificate-authority", "insecure-skip-tls-verify", "token")

	cmds.AddCommand(newExperimentalCommand(f, ioStreams))

	cmds.AddCommand(kubectlwrappers.NewCmdPlugin(f, ioStreams))
	cmds.AddCommand(version.NewCmdVersion(f, ioStreams))
	cmds.AddCommand(options.NewCmdOptions(ioStreams))

	registerCompletionFuncForGlobalFlags(cmds, f)

	return cmds
}

func runHelp(cmd *cobra.Command, args []string) {
	cmd.Help()
}

func moved(fullName, to string, parent, cmd *cobra.Command) string {
	cmd.Long = fmt.Sprintf("DEPRECATED: This command has been moved to \"%s %s\"", fullName, to)
	cmd.Short = fmt.Sprintf("DEPRECATED: %s", to)
	parent.AddCommand(cmd)
	return cmd.Name()
}

// changeSharedFlagDefaults changes values of shared flags that we disagree with.  This can't be done in godep code because
// that would change behavior in our `kubectl` symlink. Defend each change.
func changeSharedFlagDefaults(rootCmd *cobra.Command) {
	cmds := []*cobra.Command{rootCmd}

	for i := 0; i < len(cmds); i++ {
		currCmd := cmds[i]
		cmds = append(cmds, currCmd.Commands()...)

		// we want to disable the --validate flag by default when we're running kube commands from oc.  We want to make sure
		// that we're only getting the upstream --validate flags, so check both the flag and the usage
		if validateFlag := currCmd.Flags().Lookup("validate"); (validateFlag != nil) && (strings.Contains(validateFlag.Usage, "Must be one of: strict (or true), warn, ignore (or false)")) {
			validateFlag.DefValue = "ignore"
			validateFlag.Value.Set("ignore")
			validateFlag.Changed = false
		}
	}
}

func newExperimentalCommand(f kcmdutil.Factory, ioStreams genericclioptions.IOStreams) *cobra.Command {
	experimental := &cobra.Command{
		Use:   "ex",
		Short: "Experimental commands under active development",
		Long:  "The commands grouped here are under development and may change without notice.",
		Run: func(c *cobra.Command, args []string) {
			c.SetOutput(ioStreams.Out)
			c.Help()
		},
	}

	// remove this line, when adding experimental commands
	experimental.Hidden = true

	return experimental
}

// CommandFor returns the appropriate command for this base name,
// or the OpenShift CLI command.
func CommandFor(basename string) *cobra.Command {
	var cmd *cobra.Command

	in, out, err := os.Stdin, os.Stdout, os.Stderr

	// Make case-insensitive and strip executable suffix if present
	if runtime.GOOS == "windows" {
		basename = strings.ToLower(basename)
		basename = strings.TrimSuffix(basename, ".exe")
	}

	switch basename {
	case "kubectl":
		cmd = kubecmd.NewDefaultKubectlCommand()
	case "openshift-deploy":
		cmd = deployer.NewCommandDeployer(basename)
	case "openshift-recycle":
		cmd = recycle.NewCommandRecycle(basename, out)
	default:
		shimKubectlForOc()
		cmd = NewDefaultOcCommand(in, out, err)

		// treat oc as a kubectl plugin
		if strings.HasPrefix(basename, "kubectl-") {
			args := strings.Split(strings.TrimPrefix(basename, "kubectl-"), "-")

			// the plugin mechanism interprets "_" as dashes. Convert any "_" our basename
			// might have in order to find the appropriate command in the `oc` tree.
			for i := range args {
				args[i] = strings.Replace(args[i], "_", "-", -1)
			}

			if targetCmd, _, err := cmd.Find(args); targetCmd != nil && err == nil {
				// since cobra refuses to execute a child command, executing its root
				// any time Execute() is called, we must create a completely new command
				// and "deep copy" the targetCmd information to it.
				newParent := &cobra.Command{
					Use:     targetCmd.Use,
					Short:   targetCmd.Short,
					Long:    targetCmd.Long,
					Example: targetCmd.Example,
					Run:     targetCmd.Run,
				}

				// copy flags
				newParent.Flags().AddFlagSet(cmd.Flags())
				newParent.Flags().AddFlagSet(targetCmd.Flags())
				newParent.PersistentFlags().AddFlagSet(targetCmd.PersistentFlags())

				// copy subcommands
				newParent.AddCommand(targetCmd.Commands()...)
				cmd = newParent
			}
		}
	}

	if cmd.UsageFunc() == nil {
		ktemplates.ActsAsRootCommand(cmd, []string{"options"})
	}
	return cmd
}

func registerCompletionFuncForGlobalFlags(cmd *cobra.Command, f kcmdutil.Factory) {
	kcmdutil.CheckErr(cmd.RegisterFlagCompletionFunc(
		"namespace",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completion.CompGetResource(f, cmd, "namespace", toComplete), cobra.ShellCompDirectiveNoFileComp
		}))
	kcmdutil.CheckErr(cmd.RegisterFlagCompletionFunc(
		"context",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completion.ListContextsInConfig(toComplete), cobra.ShellCompDirectiveNoFileComp
		}))
	kcmdutil.CheckErr(cmd.RegisterFlagCompletionFunc(
		"cluster",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completion.ListClustersInConfig(toComplete), cobra.ShellCompDirectiveNoFileComp
		}))
	kcmdutil.CheckErr(cmd.RegisterFlagCompletionFunc(
		"user",
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completion.ListUsersInConfig(toComplete), cobra.ShellCompDirectiveNoFileComp
		}))
}
