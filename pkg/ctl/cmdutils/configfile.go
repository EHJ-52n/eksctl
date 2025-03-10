package cmdutils

import (
	"encoding/csv"
	"fmt"
	"reflect"
	"strings"

	"github.com/kris-nova/logger"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/weaveworks/eksctl/pkg/actions/irsa"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils/filter"
	"github.com/weaveworks/eksctl/pkg/eks"
	"github.com/weaveworks/eksctl/pkg/utils/names"
)

// AddConfigFileFlag adds common --config-file flag
func AddConfigFileFlag(fs *pflag.FlagSet, path *string) {
	fs.StringVarP(path, "config-file", "f", "", "load configuration from a file (or stdin if set to '-')")
}

// ClusterConfigLoader is an interface that loaders should implement
type ClusterConfigLoader interface {
	Load() error
}

type commonClusterConfigLoader struct {
	*Cmd

	flagsIncompatibleWithConfigFile    sets.String
	flagsIncompatibleWithoutConfigFile sets.String
	validateWithConfigFile             func() error
	validateWithoutConfigFile          func() error
}

var (
	defaultFlagsIncompatibleWithConfigFile = []string{
		"name",
		"region",
		"version",
		"cluster",
		"namepace",
	}
	defaultFlagsIncompatibleWithoutConfigFile = []string{
		"only",
		"include",
		"exclude",
		"only-missing",
	}

	commonCreateFlagsIncompatibleWithDryRun = []string{
		"cfn-disable-rollback",
		"cfn-role-arn",
		"install-neuron-plugin",
		"install-nvidia-plugin",
		"profile",
		"timeout",
	}

	commonNGFlagsIncompatibleWithConfigFile = []string{
		"managed",
		"spot",
		"instance-types",
		"nodes",
		"nodes-min",
		"nodes-max",
		"node-type",
		"node-volume-size",
		"node-volume-type",
		"max-pods-per-node",
		"node-ami",
		"node-ami-family",
		"ssh-access",
		"ssh-public-key",
		"enable-ssm",
		"node-private-networking",
		"node-security-groups",
		"node-labels",
		"node-zones",
		"asg-access",
		"external-dns-access",
		"full-ecr-access",
		"instance-name",
		"instance-prefix",
	}
)

func newCommonClusterConfigLoader(cmd *Cmd) *commonClusterConfigLoader {
	nilValidatorFunc := func() error { return nil }

	return &commonClusterConfigLoader{
		Cmd: cmd,

		validateWithConfigFile:             nilValidatorFunc,
		flagsIncompatibleWithConfigFile:    sets.NewString(defaultFlagsIncompatibleWithConfigFile...),
		validateWithoutConfigFile:          nilValidatorFunc,
		flagsIncompatibleWithoutConfigFile: sets.NewString(defaultFlagsIncompatibleWithoutConfigFile...),
	}
}

// Load ClusterConfig or use flags
func (l *commonClusterConfigLoader) Load() error {
	if err := api.Register(); err != nil {
		return err
	}

	if l.ClusterConfigFile == "" {
		if flagName, found := findChangedFlag(l.CobraCommand, l.flagsIncompatibleWithoutConfigFile.List()); found {
			return errors.Errorf("cannot use --%s unless a config file is specified via --config-file/-f", flagName)
		}
		return l.validateWithoutConfigFile()
	}

	var err error

	// The reference to ClusterConfig should only be reassigned if ClusterConfigFile is specified
	// because other parts of the code store the pointer locally and access it directly instead of via
	// the Cmd reference
	if l.ClusterConfig, err = eks.LoadConfigFromFile(l.ClusterConfigFile); err != nil {
		return err
	}
	meta := l.ClusterConfig.Metadata

	if meta == nil {
		return ErrMustBeSet("metadata")
	}

	if flagName, found := findChangedFlag(l.CobraCommand, l.flagsIncompatibleWithConfigFile.List()); found {
		return ErrCannotUseWithConfigFile(fmt.Sprintf("--%s", flagName))
	}

	if l.flagsIncompatibleWithConfigFile.Has("name") && l.NameArg != "" {
		return ErrCannotUseWithConfigFile("name argument")
	}

	if meta.Name == "" {
		return ErrMustBeSet("metadata.name")
	}

	if meta.Region == "" {
		return ErrMustBeSet("metadata.region")
	}
	l.ProviderConfig.Region = meta.Region

	api.SetDefaultGitSettings(l.ClusterConfig)
	return l.validateWithConfigFile()
}

func findChangedFlag(cmd *cobra.Command, flagNames []string) (string, bool) {
	for _, f := range flagNames {
		if flag := cmd.Flag(f); flag != nil && flag.Changed {
			return f, true
		}
	}
	return "", false
}

func validateMetadataWithoutConfigFile(cmd *Cmd) error {
	meta := cmd.ClusterConfig.Metadata

	if meta.Name != "" && cmd.NameArg != "" {
		return ErrClusterFlagAndArg(cmd, meta.Name, cmd.NameArg)
	}

	if cmd.NameArg != "" {
		meta.Name = cmd.NameArg
	}

	if meta.Name == "" {
		return ErrMustBeSet(ClusterNameFlag(cmd))
	}

	return nil
}

func (l *commonClusterConfigLoader) validateMetadataWithoutConfigFile() error {
	return validateMetadataWithoutConfigFile(l.Cmd)
}

// NewMetadataLoader handles loading of clusterConfigFile vs using flags for all commands that require only
// metadata fields, e.g. `eksctl delete cluster` or `eksctl utils update-kube-proxy` and other similar
// commands that do simple operations against existing clusters
func NewMetadataLoader(cmd *Cmd) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithoutConfigFile = l.validateMetadataWithoutConfigFile

	return l
}

// NewCreateClusterLoader will load config or use flags for 'eksctl create cluster'
func NewCreateClusterLoader(cmd *Cmd, ngFilter *filter.NodeGroupFilter, ng *api.NodeGroup, params *CreateClusterCmdParams) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	ngFilter.SetExcludeAll(params.WithoutNodeGroup)

	clusterFlagsIncompatibleWithConfigFile := []string{
		"tags",
		"zones",
		"fargate",
		"vpc-private-subnets",
		"vpc-public-subnets",
		"vpc-cidr",
		"vpc-nat-mode",
		"vpc-from-kops-cluster",
	}

	l.flagsIncompatibleWithConfigFile.Insert(append(clusterFlagsIncompatibleWithConfigFile, commonNGFlagsIncompatibleWithConfigFile...)...)

	l.flagsIncompatibleWithoutConfigFile.Insert("install-vpc-controllers")

	validateDryRun := func() error {
		if !params.DryRun {
			return nil
		}

		flagsIncompatibleWithDryRun := append([]string{
			"authenticator-role-arn",
			"auto-kubeconfig",
			"install-vpc-controllers",
			"kubeconfig",
			"set-kubeconfig-context",
			"write-kubeconfig",
		}, commonCreateFlagsIncompatibleWithDryRun...)

		return validateDryRunOptions(l.CobraCommand, flagsIncompatibleWithDryRun)
	}

	l.validateWithConfigFile = func() error {
		clusterConfig := l.ClusterConfig
		if clusterConfig.VPC == nil {
			clusterConfig.VPC = api.NewClusterVPC()
		}

		if clusterConfig.VPC.NAT == nil {
			clusterConfig.VPC.NAT = api.DefaultClusterNAT()
		}

		if !api.IsSetAndNonEmptyString(clusterConfig.VPC.NAT.Gateway) {
			*clusterConfig.VPC.NAT.Gateway = api.ClusterSingleNAT
		}

		if clusterConfig.PrivateCluster != nil && clusterConfig.PrivateCluster.Enabled {
			if clusterEndpoints := clusterConfig.VPC.ClusterEndpoints; clusterEndpoints != nil && (clusterEndpoints.PublicAccess != nil || clusterEndpoints.PrivateAccess != nil) {
				return errors.New("vpc.clusterEndpoints cannot be set for a fully-private cluster (privateCluster.enabled) as the endpoint access defaults to private-only")
			}
		}

		api.SetClusterEndpointAccessDefaults(clusterConfig.VPC)

		if !clusterConfig.HasClusterEndpointAccess() {
			return api.ErrClusterEndpointNoAccess
		}

		if clusterConfig.HasAnySubnets() && len(clusterConfig.AvailabilityZones) != 0 {
			return errors.New("vpc.subnets and availabilityZones cannot be set at the same time")
		}

		if clusterConfig.GitOps != nil && clusterConfig.Git != nil {
			return errors.New("git cannot be configured alongside gitops")
		}

		if clusterConfig.GitOps != nil {
			fluxCfg := clusterConfig.GitOps.Flux

			if fluxCfg != nil {
				if fluxCfg.GitProvider == "" {
					return ErrMustBeSet("gitops.flux.gitProvider")
				}
				if len(fluxCfg.Flags) == 0 {
					return ErrMustBeSet("gitops.flux.flags")
				}
			}
		}

		if clusterConfig.Git != nil {
			repo := clusterConfig.Git.Repo
			if repo != nil {
				if repo.URL == "" {
					return ErrMustBeSet("git.repo.url")
				}

				if repo.Email == "" {
					return ErrMustBeSet("git.repo.email")
				}

				profile := clusterConfig.Git.BootstrapProfile
				if profile != nil && profile.Source == "" {
					return ErrMustBeSet("git.bootstrapProfile.source")
				}
			}
		}

		return validateDryRun()
	}

	l.validateWithoutConfigFile = func() error {
		meta := l.ClusterConfig.Metadata

		// generate cluster name or use either flag or argument
		if names.ForCluster(meta.Name, l.NameArg) == "" {
			return ErrClusterFlagAndArg(l.Cmd, meta.Name, l.NameArg)
		}
		meta.Name = names.ForCluster(meta.Name, l.NameArg)

		if l.ClusterConfig.Status != nil {
			return fmt.Errorf("status fields are read-only")
		}

		if err := validateManagedNGFlags(l.CobraCommand, params.Managed); err != nil {
			return err
		}

		// prevent creation of invalid config object with irrelevant nodegroup
		// that may or may not be constructed correctly
		if !params.WithoutNodeGroup {
			if params.Managed {
				l.ClusterConfig.ManagedNodeGroups = []*api.ManagedNodeGroup{makeManagedNodegroup(ng, params.CreateManagedNGOptions)}
			} else {
				l.ClusterConfig.NodeGroups = []*api.NodeGroup{ng}
			}
		}

		api.SetClusterEndpointAccessDefaults(l.ClusterConfig.VPC)

		if params.Fargate {
			l.ClusterConfig.SetDefaultFargateProfile()
			// A Fargate-only cluster should NOT have any un-managed node group:
			l.ClusterConfig.NodeGroups = []*api.NodeGroup{}
		}

		for _, ng := range l.ClusterConfig.NodeGroups {
			// generate nodegroup name or use flag
			ng.Name = names.ForNodeGroup(ng.Name, "")
			if err := normalizeNodeGroup(ng, l); err != nil {
				return err
			}
		}

		for _, ng := range l.ClusterConfig.ManagedNodeGroups {
			if err := validateUnsupportedCLIFeatures(ng); err != nil {
				return err
			}
			ng.Name = names.ForNodeGroup(ng.Name, "")
			normalizeBaseNodeGroup(ng, l.CobraCommand)
		}

		return validateDryRun()
	}

	return l
}

func validateDryRunOptions(cmd *cobra.Command, incompatibleFlags []string) error {
	if flagName, found := findChangedFlag(cmd, incompatibleFlags); found {
		return errors.Errorf("cannot use --%s with --dry-run as this option cannot be represented in ClusterConfig", flagName)
	}
	return nil
}

// NewCreateNodeGroupLoader will load config or use flags for 'eksctl create nodegroup'
func NewCreateNodeGroupLoader(cmd *Cmd, ng *api.NodeGroup, ngFilter *filter.NodeGroupFilter, ngOptions CreateNGOptions, mngOptions CreateManagedNGOptions) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.flagsIncompatibleWithConfigFile.Insert(commonNGFlagsIncompatibleWithConfigFile...)

	validateDryRun := func() error {
		if !ngOptions.DryRun {
			return nil
		}
		// Filters (--include / --exclude) cannot be represented in ClusterConfig, however, they affect the output, so they're allowed
		flagsIncompatibleWithDryRun := append([]string{
			"update-auth-configmap",
		}, commonCreateFlagsIncompatibleWithDryRun...)

		return validateDryRunOptions(l.CobraCommand, flagsIncompatibleWithDryRun)
	}

	l.validateWithConfigFile = func() error {
		if err := ngFilter.AppendGlobs(l.Include, l.Exclude, l.ClusterConfig.GetAllNodeGroupNames()); err != nil {
			return err
		}
		return validateDryRun()
	}

	l.validateWithoutConfigFile = func() error {
		if l.ClusterConfig.Metadata.Name == "" {
			return ErrMustBeSet(ClusterNameFlag(cmd))
		}
		if err := validateManagedNGFlags(l.CobraCommand, mngOptions.Managed); err != nil {
			return err
		}
		if err := validateUnmanagedNGFlags(l.CobraCommand, mngOptions.Managed); err != nil {
			return err
		}
		if mngOptions.Managed {
			l.ClusterConfig.ManagedNodeGroups = []*api.ManagedNodeGroup{makeManagedNodegroup(ng, mngOptions)}
		} else {
			l.ClusterConfig.NodeGroups = []*api.NodeGroup{ng}
		}

		// Validate both filtered and unfiltered nodegroups
		if mngOptions.Managed {
			for _, ng := range l.ClusterConfig.ManagedNodeGroups {
				if err := validateUnsupportedCLIFeatures(ng); err != nil {
					return err
				}
				ngName := names.ForNodeGroup(ng.Name, l.NameArg)
				if ngName == "" {
					return ErrFlagAndArg("--name", ng.Name, l.NameArg)
				}
				ng.Name = ngName
				normalizeBaseNodeGroup(ng, l.CobraCommand)
			}
		} else {
			for _, ng := range l.ClusterConfig.NodeGroups {
				// generate nodegroup name or use either flag or argument
				ngName := names.ForNodeGroup(ng.Name, l.NameArg)
				if ngName == "" {
					return ErrFlagAndArg("--name", ng.Name, l.NameArg)
				}
				ng.Name = ngName
				if err := normalizeNodeGroup(ng, l); err != nil {
					return err
				}
			}
		}
		return validateDryRun()
	}

	return l
}

func makeManagedNodegroup(nodeGroup *api.NodeGroup, options CreateManagedNGOptions) *api.ManagedNodeGroup {
	ngBase := *nodeGroup.NodeGroupBase
	if ngBase.SecurityGroups != nil {
		ngBase.SecurityGroups = &api.NodeGroupSGs{
			AttachIDs: ngBase.SecurityGroups.AttachIDs,
		}
	}
	return &api.ManagedNodeGroup{
		NodeGroupBase: &ngBase,
		Spot:          options.Spot,
		InstanceTypes: options.InstanceTypes,
	}
}

func validateUnsupportedCLIFeatures(ng *api.ManagedNodeGroup) error {
	if api.IsWindowsImage(ng.AMIFamily) {
		return errors.New("Windows is not supported for managed nodegroups; eksctl now creates " +
			"managed nodegroups by default, to use a self-managed nodegroup, pass --managed=false")
	}
	return nil
}

func validateManagedNGFlags(cmd *cobra.Command, managed bool) error {
	if managed {
		return nil
	}

	flagsValidOnlyWithMNG := []string{"spot", "instance-types"}
	if flagName, found := findChangedFlag(cmd, flagsValidOnlyWithMNG); found {
		return errors.Errorf("--%s is only valid with managed nodegroups (--managed)", flagName)
	}
	return nil
}

func validateUnmanagedNGFlags(cmd *cobra.Command, managed bool) error {
	if !managed {
		return nil
	}

	flagsValidOnlyWithUnmanagedNG := []string{"version"}
	if flagName, found := findChangedFlag(cmd, flagsValidOnlyWithUnmanagedNG); found {
		return fmt.Errorf("--%s is only valid with unmanaged nodegroups", flagName)
	}
	return nil
}

func normalizeNodeGroup(ng *api.NodeGroup, l *commonClusterConfigLoader) error {
	if flag := l.CobraCommand.Flag("ssh-public-key"); flag != nil && flag.Changed {
		if *ng.SSH.PublicKeyPath == "" {
			return fmt.Errorf("--ssh-public-key must be non-empty string")
		}
		if flag := l.CobraCommand.Flag("ssh-access"); flag == nil || !flag.Changed {
			ng.SSH.Allow = api.Enabled()
		}
	} else {
		ng.SSH.PublicKeyPath = nil
	}

	if *ng.VolumeType == api.NodeVolumeTypeIO1 {
		return fmt.Errorf("%s volume type is not supported via flag --node-volume-type, please use a config file", api.NodeVolumeTypeIO1)
	}

	normalizeBaseNodeGroup(ng, l.CobraCommand)
	return nil
}

func normalizeBaseNodeGroup(np api.NodePool, cmd *cobra.Command) {
	ng := np.BaseNodeGroup()
	flags := cmd.Flags()
	if !flags.Changed("instance-selector-gpus") {
		ng.InstanceSelector.GPUs = nil
	}
	if !flags.Changed("enable-ssm") {
		ng.SSH.EnableSSM = nil
	}
}

// NewDeleteNodeGroupLoader will load config or use flags for 'eksctl delete nodegroup'
func NewDeleteNodeGroupLoader(cmd *Cmd, ng *api.NodeGroup, ngFilter *filter.NodeGroupFilter) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithConfigFile = func() error {
		return ngFilter.AppendGlobs(l.Include, l.Exclude, l.ClusterConfig.GetAllNodeGroupNames())
	}

	l.flagsIncompatibleWithoutConfigFile.Insert(
		"approve",
	)

	l.validateWithoutConfigFile = func() error {
		if l.ClusterConfig.Metadata.Name == "" {
			return ErrMustBeSet(ClusterNameFlag(cmd))
		}

		if ng.Name != "" && l.NameArg != "" {
			return ErrFlagAndArg("--name", ng.Name, l.NameArg)
		}

		if l.NameArg != "" {
			ng.Name = l.NameArg
		}

		if ng.Name == "" {
			return ErrMustBeSet("--name")
		}

		ngFilter.AppendIncludeNames(ng.Name)

		l.Plan = false

		return nil
	}

	return l
}

// NewUtilsEnableLoggingLoader will load config or use flags for 'eksctl utils update-cluster-logging'
func NewUtilsEnableLoggingLoader(cmd *Cmd) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.flagsIncompatibleWithConfigFile.Insert(
		"enable-types",
		"disable-types",
	)

	l.validateWithoutConfigFile = l.validateMetadataWithoutConfigFile

	return l
}

// NewUtilsEnableEndpointAccessLoader will load config or use flags for 'eksctl utils update-cluster-endpoints'.
func NewUtilsEnableEndpointAccessLoader(cmd *Cmd, privateAccess, publicAccess bool) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.flagsIncompatibleWithConfigFile.Insert(
		"private-access",
		"public-access",
	)
	l.validateWithoutConfigFile = func() error {
		if err := l.validateMetadataWithoutConfigFile(); err != nil {
			return err
		}

		if flag := l.CobraCommand.Flag("private-access"); flag != nil && flag.Changed {
			cmd.ClusterConfig.VPC.ClusterEndpoints.PrivateAccess = &privateAccess
		} else {
			cmd.ClusterConfig.VPC.ClusterEndpoints.PrivateAccess = nil
		}

		if flag := l.CobraCommand.Flag("public-access"); flag != nil && flag.Changed {
			cmd.ClusterConfig.VPC.ClusterEndpoints.PublicAccess = &publicAccess
		} else {
			cmd.ClusterConfig.VPC.ClusterEndpoints.PublicAccess = nil
		}

		return nil
	}
	l.validateWithConfigFile = func() error {
		if l.ClusterConfig.VPC == nil {
			l.ClusterConfig.VPC = api.NewClusterVPC()
		}
		api.SetClusterEndpointAccessDefaults(l.ClusterConfig.VPC)
		return nil
	}

	return l
}

// NewUtilsAssociateIAMOIDCProviderLoader will load config or use flags for 'eksctl utils associal-iam-oidc-provider'
func NewUtilsAssociateIAMOIDCProviderLoader(cmd *Cmd) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithoutConfigFile = func() error {
		l.ClusterConfig.IAM.WithOIDC = api.Enabled()
		return l.validateMetadataWithoutConfigFile()
	}

	l.validateWithConfigFile = func() error {
		if l.ClusterConfig.IAM == nil || api.IsDisabled(l.ClusterConfig.IAM.WithOIDC) {
			return fmt.Errorf("'iam.withOIDC' is not enabled in %q", l.ClusterConfigFile)
		}
		return nil
	}

	return l
}

// NewUtilsPublicAccessCIDRsLoader loads config or uses flags for `eksctl utils set-public-access-cidrs <cidrs>`
func NewUtilsPublicAccessCIDRsLoader(cmd *Cmd) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithConfigFile = func() error {
		if cmd.NameArg != "" {
			return fmt.Errorf("config file and CIDR list argument %s", IncompatibleFlags)
		}
		if l.ClusterConfig.VPC == nil || l.ClusterConfig.VPC.PublicAccessCIDRs == nil {
			return errors.New("field vpc.publicAccessCIDRs is required")
		}
		return nil
	}

	l.validateWithoutConfigFile = func() error {
		if cmd.NameArg == "" {
			return errors.New("a comma-separated CIDR list is required")
		}

		cidrs, err := parseList(cmd.NameArg)
		if err != nil {
			return err
		}
		l.ClusterConfig.VPC.PublicAccessCIDRs = cidrs
		return nil
	}
	return l
}

func parseList(arg string) ([]string, error) {
	reader := strings.NewReader(arg)
	csvReader := csv.NewReader(reader)
	return csvReader.Read()
}

// NewCreateIAMServiceAccountLoader will load config or use flags for 'eksctl create iamserviceaccount'
func NewCreateIAMServiceAccountLoader(cmd *Cmd, saFilter *filter.IAMServiceAccountFilter) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.flagsIncompatibleWithConfigFile.Insert(
		"policy-arn",
	)

	l.validateWithConfigFile = func() error {
		if l.ClusterConfig.IAM == nil || l.ClusterConfig.IAM.ServiceAccounts == nil {
			return fmt.Errorf("'iam.serviceAccounts' is not defined in %q", l.ClusterConfigFile)
		}
		return saFilter.AppendGlobs(l.Include, l.Exclude, l.ClusterConfig.IAM.ServiceAccounts)
	}

	l.validateWithoutConfigFile = func() error {
		if l.ClusterConfig.Metadata.Name == "" {
			return ErrMustBeSet(ClusterNameFlag(cmd))
		}

		if len(l.ClusterConfig.IAM.ServiceAccounts) != 1 {
			return fmt.Errorf("unexpected number of service accounts")
		}

		serviceAccount := l.ClusterConfig.IAM.ServiceAccounts[0]

		if serviceAccount.Name != "" && l.NameArg != "" {
			return ErrFlagAndArg("--name", serviceAccount.Name, l.NameArg)
		}

		if l.NameArg != "" {
			serviceAccount.Name = l.NameArg
		}

		if serviceAccount.Name == "" {
			return ErrMustBeSet("--name")
		}

		if len(serviceAccount.AttachPolicyARNs) == 0 && serviceAccount.AttachRoleARN == "" {
			return ErrMustBeSet("--attach-policy-arn or --attach-role-arn")
		}

		if serviceAccount.AttachRoleARN != "" && (*serviceAccount.RoleOnly || serviceAccount.RoleName != "") {
			return fmt.Errorf("cannot provde --role-name or --role-only when --attach-role-arn is configured")
		}

		if serviceAccount.AttachRoleARN != "" && (len(serviceAccount.AttachPolicyARNs) != 0 || serviceAccount.AttachPolicy != nil) {
			return fmt.Errorf("cannot provide --attach-role-arn and specify polices to attach")
		}

		return nil
	}

	return l
}

// NewGetIAMServiceAccountLoader will load config or use flags for 'eksctl get iamserviceaccount'
func NewGetIAMServiceAccountLoader(cmd *Cmd, options *irsa.GetOptions) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithConfigFile = func() error {
		if api.IsDisabled(l.ClusterConfig.IAM.WithOIDC) {
			return fmt.Errorf("'iam.withOIDC' is not enabled in %q", l.ClusterConfigFile)
		}
		return nil
	}

	l.validateWithoutConfigFile = func() error {
		if l.ClusterConfig.Metadata.Name == "" {
			return ErrMustBeSet(ClusterNameFlag(cmd))
		}
		if options.Name != "" && l.NameArg != "" {
			return ErrFlagAndArg("--name", options.Name, l.NameArg)
		}
		if options.Name == "" && cmd.NameArg != "" {
			options.Name = cmd.NameArg
		}

		l.Plan = false

		return nil
	}

	return l
}

// NewDeleteIAMServiceAccountLoader will load config or use flags for 'eksctl delete iamserviceaccount'
func NewDeleteIAMServiceAccountLoader(cmd *Cmd, sa *api.ClusterIAMServiceAccount, saFilter *filter.IAMServiceAccountFilter) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithConfigFile = func() error {
		if l.ClusterConfig.IAM == nil || api.IsDisabled(l.ClusterConfig.IAM.WithOIDC) {
			return fmt.Errorf("'iam.withOIDC' is not enabled in %q", l.ClusterConfigFile)
		}
		return saFilter.AppendGlobs(l.Include, l.Exclude, l.ClusterConfig.IAM.ServiceAccounts)
	}

	l.flagsIncompatibleWithoutConfigFile.Insert(
		"approve",
	)

	l.validateWithoutConfigFile = func() error {
		sa.AttachPolicyARNs = []string{""} // force to pass general validation

		if l.ClusterConfig.Metadata.Name == "" {
			return ErrMustBeSet(ClusterNameFlag(cmd))
		}

		if sa.Name != "" && l.NameArg != "" {
			return ErrFlagAndArg("--name", sa.Name, l.NameArg)
		}

		if l.NameArg != "" {
			sa.Name = l.NameArg
		}

		if sa.Name == "" {
			return ErrMustBeSet("--name")
		}

		l.Plan = false

		return nil
	}

	return l
}

// NewUpdateNodegroupLoader will load config or use flags for 'eksctl update nodegroup'.
func NewUpdateNodegroupLoader(cmd *Cmd) ClusterConfigLoader {
	l := newCommonClusterConfigLoader(cmd)

	l.validateWithConfigFile = func() error {
		length := len(l.ClusterConfig.ManagedNodeGroups)
		if length < 1 {
			return ErrMustBeSet("managedNodeGroups field")
		}

		for _, ng := range l.ClusterConfig.ManagedNodeGroups {
			logger.Info("validating nodegroup %q", ng.Name)

			var unsupportedFields []string
			var err error
			if unsupportedFields, err = validateSupportedConfigFields(*ng.NodeGroupBase, []string{"Name"}, unsupportedFields); err != nil {
				return err
			}

			if unsupportedFields, err = validateSupportedConfigFields(*ng, []string{"NodeGroupBase", "UpdateConfig"}, unsupportedFields); err != nil {
				return err
			}

			if len(unsupportedFields) > 0 {
				logger.Warning("unchanged fields for nodegroup %s: the following fields remain unchanged; they are not supported by `eksctl update nodegroup`: %s", ng.Name, strings.Join(unsupportedFields[:], ", "))
			}
		}

		return nil
	}

	l.validateWithoutConfigFile = func() error {
		if cmd.ClusterConfigFile == "" {
			return ErrMustBeSet("--config-file")
		}
		return nil
	}
	return l
}

// validateSupportedConfigFields parses a config file's fields, evaluates if non-empty fields are supported,
// and returns an error if a field is not supported.
func validateSupportedConfigFields(obj interface{}, supportedFields []string, unsupportedFields []string) ([]string, error) {
	v := reflect.ValueOf(obj)
	t := v.Type()
	for fieldNumber := 0; fieldNumber < v.NumField(); fieldNumber++ {
		if !emptyConfigField(v.Field(fieldNumber)) {
			if !contains(supportedFields, t.Field(fieldNumber).Name) {
				unsupportedFields = append(unsupportedFields, t.Field(fieldNumber).Name)
			}
		}
	}
	return unsupportedFields, nil
}

// emptyConfigField parses a field's value according to its value then returns true
// if it is not empty/zero/nil.
func emptyConfigField(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.String:
		return v.String() == ""
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan:
		return v.IsNil()
	case reflect.Bool:
		return !v.Bool()
	}
	return false
}

func contains(supportedFields []string, fieldName string) bool {
	for _, f := range supportedFields {
		if f == fieldName {
			return true
		}
	}
	return false
}
