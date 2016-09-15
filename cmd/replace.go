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
	Run: func(cmd *cobra.Command, args []string) {
		runReplace(&_replaceConfig, cmd, args )
	},
}

type ReplaceConfig struct {
	xrFile string
	version string
	targetNamespace string
	namePrefix string
	labels string
	clean bool
}

var _replaceConfig ReplaceConfig

func runReplace(config *ReplaceConfig, cmd *cobra.Command, args []string) {

	if config.xrFile == "" {
		Out.Error( "--config must be specified" )
		cmd.Help()
		os.Exit(1)
	}

	xr, err := ReadXR( config.xrFile )
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

	if persist,_ := RootCmd.PersistentFlags().GetBool("preserve-git"); persist {
		Out.Warn( "The working git directory will not be removed: %v", git.repoDir )
	} else {
		defer os.RemoveAll( git.repoDir )
	}

	if config.version == "" {
		config.version = xr.Spec.DefaultVersion
		if config.version == "" {
			config.version = "master"
		}
	}

	branchName := xr.Spec.Git.Branch.Prefix + config.version

	_,se,err := git.Exec( "checkout", branchName )

	if err != nil {
		Out.Error( "Error checking out git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	if config.targetNamespace == "" {
		config.targetNamespace = xr.Spec.ImportRules.Namespace
		if config.targetNamespace == "" {
			config.targetNamespace = projectName
		}
	}

	setNS := "--namespace=" + config.targetNamespace

	// Delete any object that was created by the XR previously if --clean was specified
	if config.clean {
		OC.Exec( "delete", "all", setNS,  "-l", LABEL_REPOSITORY + "=" + xr.Metadata.Name )
	}

	namePrefix := xr.Spec.ImportRules.Transforms.NamePrefix
	if config.namePrefix != "" {
		namePrefix = config.namePrefix
	}

	err = RunPatches( xr, git.objectDir )
	if err != nil {
		Out.Error( "Error executing import patches: %v", err )
		os.Exit(1)
	}


	for _, filename := range FindAllKindFiles( git.objectDir ) {
		fullName := GetFullObjectNameFromPath( filename )

		if xr.Spec.ImportRules.Include != "" {
			if ! IsMatchedByKindNameList( fullName, xr.Spec.ImportRules.Include ) {
				Out.Info( "Imported resource is not selected by Include: %v", fullName )
				continue
			}
		}

		if IsMatchedByKindNameList( fullName, xr.Spec.ImportRules.Exclude ) {
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
		SetLabel( obj, LABEL_REPOSITORY_VERSION, config.version )

		for _,label := range strings.Split(config.labels, "," ) {
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

		kind := GetJSONPath( obj, "kind" ).(string)

		// Rewrite image references
		if kind == KIND_DC || kind == KIND_RC {
			containers := GetJSONPath( obj, "spec", "template", "spec", "containers" )
			if containers != nil {
				VisitJSONArrayElements( containers, func( entry interface{} ) (interface{}) {
					imageObj := GetJSONPath( entry, "image" )
					if imageObj != nil {
						image := imageObj.(string)
						registryHost, namespace, repository, tag, err := ParseDockerImageRef( image )

						if err != nil {
							Out.Error( "Invalid docker image reference in %v: %v", fullName, image )
							os.Exit(1)
						}

						for _,mapping := range xr.Spec.ImportRules.Transforms.ImageMappings {
							ok, err := dockerPatternMatches( image, mapping.Pattern, "172.", projectName )
							if err != nil {
								Out.Error( "Invalid docker image mapping pattern: %v", mapping.Pattern )
								os.Exit(1)
							}

							if ok {
								var newRef string
								newRef += mapDockerComponentWithSuffix(registryHost, mapping.SetRegistryHost, "/" )
								newRef += mapDockerComponentWithSuffix(namespace, mapping.SetNamespace, "/" )
								newRef += mapDockerComponent(repository, mapping.SetRepository )
								newRef += mapDockerTagComponentWithPrefix(tag, mapping.SetTag )
								Out.Info( "Mapping image reference in %v: %q -> %q", fullName, image, newRef )
								SetJSONObj( entry, "image", newRef )
								break // Only perform one mapping. The first one that matches.
							}
						}
					}
					return entry
				})
			}
		}

		objData, err := json.MarshalIndent( obj, "", "\t" )
		if err != nil {
			Out.Error( "Error marshalling object data (%v): %v", err, obj )
			os.Exit(1)
		}

		err = ioutil.WriteFile( filename, objData, 0600 )
		if err != nil {
			Out.Error( "Error writing object data to file (%v) [%v]", filename, err )
			os.Exit(1)
		}

		Out.Info( "Replacing %v", fullName )
		_,se,err = OC.Exec( "replace", setNS, "--cascade=true", "--force", "-f", filename )

		if err != nil {
			Out.Error( "Error while replacing object definition (%v) [%v]: %v", fullName, err, se )
			os.Exit(1)
		}
	}

}

func init() {
	RootCmd.AddCommand(replaceCmd)
	replaceCmd.Flags().StringVar(&_replaceConfig.xrFile, "config", "", "Path to ObjectRepository JSON file")
	replaceCmd.Flags().StringVar(&_replaceConfig.version, "from", "", "Version to import")
	replaceCmd.Flags().StringVar(&_replaceConfig.targetNamespace, "target-namespace", "", "Target namespace if not current")
	replaceCmd.Flags().StringVar(&_replaceConfig.namePrefix, "name-prefix", "", "Name prefix for objects being created")
	replaceCmd.Flags().StringVar(&_replaceConfig.labels, "labels", "", "New labels for objects being created")
	replaceCmd.Flags().BoolVar(&_replaceConfig.clean, "clean", false, "Removes any prior resources by the config")

}
