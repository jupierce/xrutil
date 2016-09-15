package cmd

import (
	"os"
	"encoding/json"
	"io/ioutil"
	"github.com/spf13/cobra"
	"path/filepath"
	"strings"
	"fmt"
	"time"
)

type ExportConfig struct {
	xrFile string
	version string
	message string
	overwrite bool
}

var _exportConfig ExportConfig

// exportCmd represents the export command
var exportCmd = &cobra.Command{
	Use:   "export <object-repository-json-file>",
	Short: "Exports a selection of OpenShift oject definitions",
	Run: func( cmd *cobra.Command, args []string) {
		runExport( &_exportConfig, cmd, args )
	},
}

func runExport(config *ExportConfig, cmd *cobra.Command, args []string) {

	if len( args ) == 0 {
		Out.Error( "An ObjectRepository JSON definition must be specified" )
		cmd.Help()
		os.Exit(1)
	}
	config.xrFile = args[0]


	xr, err := ReadXR( config.xrFile )
	if err != nil {
		Out.Error( "Unable to load configuration: %v", err )
		os.Exit(1)
	}

	if config.version == "" {
		config.version = xr.Spec.DefaultVersion
		if config.version == "" {
			config.version = "master"
		}
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

	branchName := xr.Spec.Git.Branch.Prefix + config.version

	// See if branch name already exists
	_,se,err := git.Exec( "checkout", branchName  )

	if err == nil  && !config.overwrite {
		Out.Error( "Branch already exists and --overwrite was not specified (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	_,se,err = git.Exec( "branch", branchName, xr.Spec.Git.Branch.BaseRef )
	if err != nil {
		Out.Warn( "Error while creating branch (%v) [%v]: %v", branchName, err, se )
	}

	_,se,err = git.Exec( "checkout", branchName )

	if err != nil {
		Out.Error( "Error checking out git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	headCommitId,se,err := git.Exec( "rev-parse", "HEAD" ) // Store the current HEAD commit ID

	if err != nil {
		Out.Error( "Unable to determine HEAD commit id for branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	_,se,err = git.Exec( "reset", "--hard", xr.Spec.Git.Branch.BaseRef )

	if err != nil {
		Out.Error( "Error hard reseting git branch (%v) to (%v) [%v]: %v", branchName, xr.Spec.Git.Branch.BaseRef, err, se )
		os.Exit(1)
	}

	_,se,err = git.Exec( "reset", "--soft", headCommitId )

	if err != nil {
		Out.Error( "Error soft reseting git branch (%v) to (%v) [%v]: %v", branchName, headCommitId, err, se )
		os.Exit(1)
	}

	projectName, err := OC.Project()
	if err != nil {
		Out.Error( "Unable to obtain current project name: %v", err )
	}

	generatedTag := fmt.Sprintf( ":%v_%v", config.version, makeTimestamp() )

	var selectedNames map[string]struct{} // nil is effectively selecting all

	if len( xr.Spec.ExportRules.Selectors ) > 0 {
		selectedNames = make(map[string]struct{})

		for _,selector := range xr.Spec.ExportRules.Selectors {
			if len( selector.MatchExpressions ) != 0 {
				Out.Error( "Selectors/MatchExpressions are not currently supported")
				os.Exit(1)
			}
			if selector.Namespace != "" {
				Out.Error( "Selectors/Namespace is not currently supported")
				os.Exit(1)
			}
			so, se, err := OC.Exec( "get", "all",  "-o=name", "-l", strings.Join( selector.MatchLabels, "," ) )
			if err != nil {
				Out.Error( "Error gathering selection [%v]: %v", err, se )
				os.Exit(1)
			}
			for _,selectedName :=  range strings.Split( so, "\n" ) {
				selectedNames[ NormalizeType( selectedName ) ] = struct{}{}
			}
		}
	}

	if xr.Spec.ExportRules.Include == "" {
		xr.Spec.ExportRules.Include = "all"
	}

	include := ToKindNameList(xr.Spec.ExportRules.Include)
	for _, i := range include {
		so, se, err := OC.Exec("export", i, "-o=json", "--exact", "--as-template=x")
		if err != nil {
			Out.Warn( "Unable to export object definitions %v [%v]: %v", i, err, se )
		}

		var template Template
		json.Unmarshal( []byte(so), &template )

		for _, ao := range template.Objects {
			obj := ao.(map[string]interface{})
			kind := pluralizeKind( obj["kind"].(string) )

			if kind == "" {
				Out.Error( "Selected object does not specify kind: %v", obj )
				os.Exit(1)
			}


			metadata := obj["metadata"].(map[string]interface{})
			name := metadata["name"].(string)

			if name == "" {
				Out.Error( "Selected object does not specify metadata.name: %v", obj )
				os.Exit(1)
			}

			fullName := NormalizeType( strings.Join( []string{ kind, name}, "/" ) )

			if selectedNames != nil {
				_, ok := selectedNames[ fullName ]
				if !ok {
					Out.Info( "Selectors matched item not in includes: %v", fullName )
					continue
				}
			}

			if IsMatchedByKindNameList( fullName, xr.Spec.ExportRules.Exclude ) {
				Out.Info( "Excluding: %v", fullName )
				continue
			}

			if ! IsMatchedByKindNameList( fullName, xr.Spec.ExportRules.Transforms.PreserveMutators ) {

				// Disallow build related artifacts from being exported
				switch kind {
				case KIND_IS:
					fallthrough
				case KIND_BC:
					Out.Error( "Selected object contains or is a mutator which is not specified in preserveMutators field: %v", fullName )
					os.Exit(1)
				}

				// Remove ImageChange triggers
				if kind == KIND_DC {
					triggers := GetJSONPath( obj, "spec", "triggers" )
					if triggers != nil {
						triggers = VisitJSONArrayElements( triggers, func( entry interface{} ) (interface{}) {
							t,ok := GetJSONPath( entry, "type" ).(string)
							if ok && t == "ImageChange" { // Strip ImageChange from resulting array
								return nil
							}
							return entry
						})
						SetJSONPath( obj, []string{ "spec", "triggers" }, triggers )
					}
				}

			}

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

							for _,mapping := range xr.Spec.ExportRules.Transforms.ImageMappings {
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
									switch mapping.TagType {
									case "user":
										newRef += mapDockerTagComponentWithPrefix(tag, mapping.SetTag )
									case "generated":
										// Formulate a highly unique tag
										newRef += generatedTag
									default:
										Out.Error( "ImageMapping tagType not presently supported: %v", mapping.TagType )
										os.Exit(1)
									}

									Out.Info( "Mapping image reference in %v: %q -> %q", fullName, image, newRef )
									SetJSONObj( entry, "image", newRef )

									_,se,err = Exec( "docker", "tag", image, newRef )
									if err != nil {
										Out.Error( "Error tagging docker image (%v) as (%v) [%v]: %v", image, newRef, err, se )
										os.Exit(1)
									}

									if mapping.Secret != "" {
										Out.Error( "Docker secrets are not presently supported; log into the necessary docker registries from the command line for push operations" )
										os.Exit(1)
									}

									if mapping.Push == nil || *mapping.Push {
										_,se,err = Exec( "docker", "push", newRef )
										if err != nil {
											Out.Error( "Error pushing docker image (%v) as newly tagged (%v) [%v]: %v", image, newRef, err, se )
											Out.Error( "Make sure you are logged into the destination registry")
											os.Exit(1)
										}
									}

									break // Only perform one mapping. The first one that matches.
								}
							}
						}
						return entry
					})
				}
			}

			Out.Info( "Exporting: %v", fullName )


			kindDir := filepath.Join( git.objectDir, kind )
			err = os.MkdirAll( kindDir, 0700 )
			if err != nil {
				Out.Error( "Error creating object directory (%v): %v", kindDir, err )
				os.Exit(1)
			}

			SetLabel( obj, LABEL_REPOSITORY, xr.Metadata.Name )
			SetLabel( obj, LABEL_REPOSITORY_VERSION, config.version )

			objectFilePath := filepath.Join( kindDir, name + ".json" )
			objData, err := json.MarshalIndent( obj, "", "\t" )

			if err != nil {
				Out.Error( "Error marshalling object data (%v): %v", err, obj )
				os.Exit(1)
			}

			err = ioutil.WriteFile( objectFilePath, objData, 0600 )
			if err != nil {
				Out.Error( "Error writing object data to file (%v) [%v]", objectFilePath, err )
				os.Exit(1)
			}
		}
	}

	err = RunPatches( xr, git.objectDir )
	if err != nil {
		Out.Error( "Error executing export patches: %v", err )
		os.Exit(1)
	}

	_,se,err = git.Exec( "add", "." )

	if err != nil {
		Out.Error( "Error adding tracked files to git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	if config.message == "" {
		config.message = fmt.Sprintf( "Version: %v (tag=%v) (date=%v)", config.version, generatedTag, time.Now().Format(time.UnixDate) )
	}

	_,se,err = git.Exec( "commit", "-m", config.message )

	if err != nil {
		Out.Error( "Error committing files to git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	_,se,err = git.Exec( "push", "--set-upstream", "origin", branchName )

	if err != nil {
		Out.Error( "Error pushing git branch (%v) [%v]: %v", branchName, err, se )
		os.Exit(1)
	}

	Out.Info( "Export complete.")
}


func init() {
	RootCmd.AddCommand(exportCmd)
	exportCmd.Flags().StringVar(&_exportConfig.version, "to", "", "Version to export")
	exportCmd.Flags().StringVar(&_exportConfig.message, "message", "", "Message for commits")
	exportCmd.Flags().BoolVar(&_exportConfig.overwrite, "overwrite", false, "Specify to permit branch overwrites")
}
