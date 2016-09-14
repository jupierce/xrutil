package cmd

import (
	"github.com/spf13/cobra"
	"os"
	"io/ioutil"
	"encoding/json"
	"strings"
)

// replaceCmd represents the replace command
var replaceCmd = &cobra.Command{
	Use:   "replace",
	Short: "Imports a set of object definitions into OpenShift",
	Run: runReplace,
}

type ReplaceConfig struct {
	xrFile string
	version string
	targetNamespace string
	namePrefix string
	labels string
	clean bool
}

var replaceConfig ReplaceConfig

func runReplace(cmd *cobra.Command, args []string) {

	if replaceConfig.xrFile == "" {
		Out.Error( "--config must be specified" )
		cmd.Help()
		os.Exit(1)
	}

	xr, err := ReadXR( replaceConfig.xrFile )
	if err != nil {
		Out.Error( "Unable to load configuration: %v", err )
		os.Exit(1)
	}

	projectName, err := OC.Project()
	if err != nil {
		Out.Error( "Unable to find current project name: %v", err )
		os.Exit(1)
	}

	git, err := PrepGitDir( xr )

	if err != nil {
		Out.Error( "Error initializing git repository: %v", err )
		os.Exit(1)
	}

	// Out.Warn( "Not presently cleaning up gitDir: %v", gitDir)
	defer os.RemoveAll( git.repoDir )

	branchName := xr.Spec.Git.Branch.Prefix + exportConfig.version

	_,se,err := git.Exec( "checkout", branchName )

	if err != nil {
		Out.Error( "Error checking out git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	if replaceConfig.targetNamespace == "" {
		replaceConfig.targetNamespace = xr.Spec.ImportRules.Namespace
		if replaceConfig.targetNamespace == "" {
			replaceConfig.targetNamespace = projectName
		}
	}

	setNS := "--namespace=" + replaceConfig.targetNamespace

	// Delete any object that was created by the XR previously if --clean was specified
	if replaceConfig.clean {
		OC.Exec( "delete", "all", setNS,  "-l", LABEL_REPOSITORY + "=" + xr.Metadata.Name )
	}

	namePrefix := xr.Spec.ImportRules.Transforms.NamePrefix
	if replaceConfig.namePrefix != "" {
		namePrefix = replaceConfig.namePrefix
	}

	err = RunPatches( xr, git.objectDir )
	if err != nil {
		Out.Error( "Error executing import patches: %v", err )
		os.Exit(1)
	}


	for _, filename := range FindAllKindFiles( git.objectDir ) {
		fullName := GetFullObjectNameFromPath( filename )

		if xr.Spec.ImportRules.Include != "" {
			if ! IsSelectedByKindNameList( fullName, xr.Spec.ImportRules.Include ) {
				Out.Info( "Imported resource is not selected by Include: %v", fullName )
				continue
			}
		}

		if IsSelectedByKindNameList( fullName, xr.Spec.ImportRules.Exclude ) {
			Out.Info( "Imported resource is exlcuded by Exclude: %v", fullName )
			continue
		}

		jsonString, err := ioutil.ReadFile( filename )

		if err != nil {
			Out.Error( "Error reading imported file (%v) [%v]", filename, err )
			os.Exit(1)
		}

		var obj interface{}
		err = json.Unmarshal( jsonString, &obj )
		if err != nil {
			Out.Error( "Error parsing imported file (%v) [%v]", filename, err )
			os.Exit(1)
		}

		if namePrefix != "" {
			newName := namePrefix + GetJSONPath( obj, "metadata", "name" ).(string)
			SetJSONPath( obj, []string{ "metadata", "name" }, newName )
			// TODO: need to check for any references to objects which need to change
		}

		SetLabel( obj, LABEL_REPOSITORY, xr.Metadata.Name )
		SetLabel( obj, LABEL_REPOSITORY_VERSION, replaceConfig.version )

		for _,label := range strings.Split(replaceConfig.labels, "," ) {
			label = strings.TrimSpace( label )
			if label != "" {
				components := strings.Split( label, "=" )
				if len( components ) != 2 {
					Out.Error( "Invalid label specified (must be <key>=<value>: %q", label )
					os.Exit(1)
				}
				SetLabel( obj, components[0], components[1] )
			}
		}

		Out.Info( "Replacing %v", fullName )
		OC.Exec( "replace", setNS, "--cascade=true", "--force", "-f", filename )
	}

}

func init() {
	RootCmd.AddCommand(replaceCmd)
	replaceCmd.Flags().StringVar(&replaceConfig.xrFile, "config", "", "Path to ObjectRepository JSON file")
	replaceCmd.Flags().StringVar(&replaceConfig.version, "from", "", "Version to import")
	replaceCmd.Flags().StringVar(&replaceConfig.targetNamespace, "target-namespace", "", "Target namespace if not current")
	replaceCmd.Flags().StringVar(&replaceConfig.namePrefix, "name-prefix", "", "Name prefix for objects being created")
	replaceCmd.Flags().StringVar(&replaceConfig.labels, "labels", "", "New labels for objects being created")
	replaceCmd.Flags().BoolVar(&replaceConfig.clean, "clean", false, "Removes any prior resources by the config")

}
