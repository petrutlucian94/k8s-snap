package k8sd_members

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	cmdutil "github.com/canonical/k8s/cmd/util"
	"github.com/canonical/k8s/pkg/log"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v2"


	"github.com/canonical/lxd/shared"
	cli "github.com/canonical/lxd/shared/cmd"
	"github.com/canonical/lxd/shared/termios"
	"github.com/canonical/microcluster/v2/client"
	"github.com/canonical/microcluster/v2/cluster"
	"github.com/canonical/microcluster/v2/microcluster"
)

// TODO: we're only handling the k8sd database for the time being. Shoud
// we also recover the k8s-dqlite database? What if this instance has the
// most recent k8sd database but not the most recent k8s-dqlite db?
const recoveryConfirmation = `You should only run this command if:
 - A quorum of cluster members is permanently lost
 - You are *absolutely* sure all k8s daemons are stopped (sudo snap stop k8s)
 - This instance has the most up to date database

Do you want to proceed? (yes/no): `

const recoveryYamlComment = `# Member roles can be modified. Unrecoverable nodes should be given the role "spare".
#
# "voter" - Voting member of the database. A majority of voters is a quorum.
# "stand-by" - Non-voting member of the database; can be promoted to voter.
# "spare" - Not a member of the database.
#
# The edit is aborted if:
# - the number of members changes
# - the name of any member changes
# - the ID of any member changes
# - the address of any member changes
# - no changes are made
`

type CommonOpts struct {
	logDebug                            bool
	logVerbose                          bool
	logLevel                            int
	stateDir                            string
}

type cmdClusterMembersList struct {
	common *CommonOpts

	flagFormat string
	flagLocal  bool
}

func (c *cmdClusterMembersList) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <address>",
		Short: "List cluster members locally, or remotely if an address is specified",
		RunE:  c.Run,
	}

	cmd.Flags().StringVarP(&c.flagFormat, "format", "f", cli.TableFormatTable, "Format (csv|json|table|yaml|compact)")
	cmd.Flags().BoolVarP(&c.flagLocal, "local", "", false, "provide only the locally available cluster info (no database query)")

	return cmd
}

func (c *cmdClusterMembersList) Run(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return cmd.Help()
	}

	// Get all state information.
	options := microcluster.Args{
		StateDir: c.common.stateDir,
	}
	m, err := microcluster.App(options)
	if err != nil {
		return err
	}

	if c.flagLocal {
		return c.listLocalClusterMembers(m)
	}

	var client *client.Client

	// Get a local client connected to the unix socket if no address is specified.
	if len(args) == 1 {
		client, err = m.RemoteClient(args[0])
		if err != nil {
			return err
		}
	} else {
		client, err = m.LocalClient()
		if err != nil {
			return err
		}
	}

	return c.listClusterMembers(cmd.Context(), client)
}

func (c *cmdClusterMembersList) listClusterMembers(ctx context.Context, client *client.Client) error {
	clusterMembers, err := client.GetClusterMembers(ctx)
	if err != nil {
		return err
	}

	data := make([][]string, len(clusterMembers))
	for i, clusterMember := range clusterMembers {
		fingerprint, err := shared.CertFingerprintStr(clusterMember.Certificate.String())
		if err != nil {
			continue
		}

		data[i] = []string{clusterMember.Name, clusterMember.Address.String(), clusterMember.Role, fingerprint, string(clusterMember.Status)}
	}

	header := []string{"NAME", "ADDRESS", "ROLE", "FINGERPRINT", "STATUS"}
	sort.Sort(cli.SortColumnsNaturally(data))

	return cli.RenderTable(c.flagFormat, header, data, clusterMembers)
}

func (c *cmdClusterMembersList) listLocalClusterMembers(m *microcluster.MicroCluster) error {
	members, err := m.GetDqliteClusterMembers()
	if err != nil {
		return err
	}

	data := make([][]string, len(members))
	for i, member := range members {
		data[i] = []string{strconv.FormatUint(member.DqliteID, 10), member.Name, member.Address, member.Role}
	}

	header := []string{"DQLITE ID", "NAME", "ADDRESS", "ROLE"}
	sort.Sort(cli.SortColumnsNaturally(data))

	return cli.RenderTable(c.flagFormat, header, data, members)
}

type cmdClusterMemberRemove struct {
	common *CommonOpts

	flagForce bool
}

func (c *cmdClusterMemberRemove) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove the specified cluster member",
		RunE:  c.Run,
	}

	cmd.Flags().BoolVarP(&c.flagForce, "force", "f", false, "Forcibly remove the cluster member")

	return cmd
}

func (c *cmdClusterMemberRemove) Run(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return cmd.Help()
	}

	options := microcluster.Args{
		StateDir: c.common.stateDir,
	}
	m, err := microcluster.App(options)
	if err != nil {
		return err
	}

	client, err := m.LocalClient()
	if err != nil {
		return err
	}

	err = client.DeleteClusterMember(context.Background(), args[0], c.flagForce)
	if err != nil {
		return err
	}

	return nil
}

type cmdClusterRecover struct {
	common *CommonOpts
}

func (c *cmdClusterRecover) command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Recover the cluster from this member if quorum is lost",
		RunE:  c.run,
	}

	return cmd
}

func (c *cmdClusterRecover) run(cmd *cobra.Command, args []string) error {
	m, err := microcluster.App(
		microcluster.Args{
			StateDir: c.common.stateDir,
		},
	)
	if err != nil {
		return err
	}

	var content []byte
	if !termios.IsTerminal(unix.Stdin) {
		content, err = io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
	} else {
		members, err := m.GetDqliteClusterMembers()
		if err != nil {
			return err
		}

		membersYaml, err := yaml.Marshal(members)
		if err != nil {
			return err
		}

		reader := bufio.NewReader(os.Stdin)
		fmt.Print(recoveryConfirmation)

		input, _ := reader.ReadString('\n')
		input = strings.TrimSuffix(input, "\n")

		if strings.ToLower(input) != "yes" {
			fmt.Println("Cluster edit aborted; no changes made")
			return nil
		}

		content, err = shared.TextEditor("", append([]byte(recoveryYamlComment), membersYaml...))
		if err != nil {
			return err
		}
	}

	newMembers := []cluster.DqliteMember{}
	err = yaml.Unmarshal(content, &newMembers)
	if err != nil {
		return err
	}

	tarballPath, err := m.RecoverFromQuorumLoss(newMembers)
	if err != nil {
		return fmt.Errorf("Cluster edit: %w", err)
	}

	fmt.Printf("Cluster changes applied; new database state saved to %s\n\n", tarballPath)
	fmt.Printf("*Before* starting any cluster member, copy %s to %s on all remaining cluster members.\n\n", tarballPath, tarballPath)
	fmt.Printf("k8sd will load this file during startup.\n")

	return nil
}

func NewRootCmd(env cmdutil.ExecutionEnvironment) *cobra.Command {
	commonOpts := CommonOpts{}

	cmd := &cobra.Command{
		Use:   "k8sd_members",
		Short: "Manage k8sd cluster members.",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}

	cmd.PersistentFlags().IntVarP(&commonOpts.logLevel, "log-level", "l", 0, "Log level")
	cmd.PersistentFlags().BoolVarP(&commonOpts.logDebug, "debug", "d", false, "Show all debug messages")
	cmd.PersistentFlags().BoolVarP(&commonOpts.logVerbose, "verbose", "v", true, "Show all information messages")
	cmd.PersistentFlags().StringVar(&commonOpts.stateDir, "state-dir", "", "Directory with the dqlite datastore")

	log.Configure(log.Options{
		LogLevel:     commonOpts.logLevel,
		AddDirHeader: true,
	})

	if commonOpts.stateDir == "" {
		commonOpts.stateDir = env.Snap.K8sdStateDir()
		log.L().Info("No state dir specified, automatically detected snap state dir.", commonOpts.stateDir)
	}

	cmd.SetIn(env.Stdin)
	cmd.SetOut(env.Stdout)
	cmd.SetErr(env.Stderr)

	var cmdRemove = cmdClusterMemberRemove{common: &commonOpts}
	cmd.AddCommand(cmdRemove.Command())

	var cmdList = cmdClusterMembersList{common: &commonOpts}
	cmd.AddCommand(cmdList.Command())

	var cmdEdit = cmdClusterRecover{common: &commonOpts}
	cmd.AddCommand(cmdEdit.command())

	return cmd
}
