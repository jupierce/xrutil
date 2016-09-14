package cmd

import (
	"os"
	"encoding/json"
	"io/ioutil"
	"github.com/spf13/cobra"
	"path/filepath"
	"strings"
	"fmt"
)

type exportCfg struct {
	xrFile string
	version string
}

var cfg exportCfg

// exportCmd represents the export command
var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Exports a selection of OpenShift oject definitions",
	Run: runExport,
}

func runExport(cmd *cobra.Command, args []string) {
	if cfg.xrFile == "" {
		Out.Error( "--config must be specified" )
		cmd.Help()
		os.Exit(1)
	}

	xrString, err := ioutil.ReadFile( cfg.xrFile )
	if err != nil {
		Out.Error( "Unable to read config file (%v): %v", cfg.xrFile, err )
		os.Exit(1)
	}

	var xr XR
	err = json.Unmarshal(xrString, &xr)
	if err != nil {
		Out.Error( "Error reading read config file (%v): %v", cfg.xrFile, err )
		os.Exit(1)
	}

	if xr.Spec.Type != "git" || xr.Spec.Git.Format != "json" {
		Out.Error( "Only git/json ObjectRepositories are presently supported")
		os.Exit(1)
	}

	if xr.Spec.Git.URI == "" {
		Out.Error( "No Git URI specified")
		os.Exit(1)
	}

	gitDir, err := ioutil.TempDir("", "xrgit")
	if err != nil {
		Out.Error( "Error creating temporary directory for git operations: %v", err )
		os.Exit(1)
	}

	Out.Info( "Cloning %v", xr.Spec.Git.URI )
	Git.Exec( "clone", "--", xr.Spec.Git.URI, gitDir )

	branchName := xr.Spec.Git.Branch.Prefix + cfg.version
	Git.Exec( "checkout", branchName )


	Out.Warn( "Not presently cleaning up gitDir: %v", gitDir)
	// defer os.RemoveAll( gitDir)

	projectName, se, err := OC.Exec("project", "-q")
	if err != nil {
		Out.Error( "Unable to obtain current project name [%v]: %v", err, se )
	}

	namesToExclude := FindLiveKindNameMap( xr.Spec.ExportRules.Exclude )
	preserveMutators := FindLiveKindNameMap( xr.Spec.ExportRules.Transforms.PreserveMutators )
	generatedTag := fmt.Sprintf( ":%v_%v", cfg.version, makeTimestamp() )


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
		so, se, err := OC.Exec("export", i, "-o=json", "--as-template=x")
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

			_, ok := namesToExclude[ fullName ]
			if ok  { // The kind/name is to be excluded
				Out.Info( "Excluding: %v", fullName )
				continue
			}

			_, preserveMutation := preserveMutators[ fullName ]

			if ! preserveMutation {

				// Disallow build related artifacts from being exported
				switch kind {
				case KIND_IS:
					fallthrough
				case KIND_BC:
					Out.Error( "Selected object contain mutator which is not specified as preserved: %v", fullName )
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
									newRef += mapDockerComponentWithSuffix(registryHost, mapping.NewRegistryHost, "/" )
									newRef += mapDockerComponentWithSuffix(namespace, mapping.NewNamespace, "/" )
									newRef += mapDockerComponent(repository, mapping.NewRepository )
									switch mapping.TagType {
									case "user":
										newRef += mapDockerTagComponentWithPrefix(tag, mapping.NewTag )
									case "generated":
										// Formulate a highly unique tag
										newRef += generatedTag
									default:
										Out.Error( "ImageMapping tagType not presently supported: %v", mapping.TagType )
										os.Exit(1)
									}
									if mapping.TagType == "user" {

									}
									Out.Info( "Mapping image reference in %v: %q -> %q", fullName, image, newRef )
									SetJSONObj( entry, "image", newRef )

									Out.Warn( "Tagging and pushing is not yet implemented!!")

									break // Only perform one mapping. The first one that matches.
								}
							}
						}
						return entry
					})
				}
			}

			Out.Info( "Exporting: %v", fullName )

			kindDir := filepath.Join( gitDir, kind )
			err = os.MkdirAll( kindDir, 0600 )
			if err != nil {
				Out.Error( "Error creating object directory directory (%v): %v", kindDir, err )
				os.Exit(1)
			}

			objectFilePath := filepath.Join( kindDir, name + ".json" )
			objData, err := json.Marshal( obj )

			if err != nil {
				Out.Error( "Error marshalling object data (%v): %v", err, obj )
				os.Exit(1)
			}

			ioutil.WriteFile( objectFilePath, objData, 0600 )
		}
	}

	for _, patch := range xr.Spec.ExportRules.Transforms.Patches {
		if patch.Type != "jq" {
			Out.Error( "Patch type is not supported: %v", patch.Type )
			Out.Error( "Currently supproted: jq" )
			os.Exit(1)
		}
		for _,fileToPatch := range FindKindNameFiles( gitDir, patch.Match ) {
			so, se, err := Exec( "jq", patch.Patch, fileToPatch )
			if err != nil {
				Out.Error("Error running jq patch operation on %v [%v]: %v", fileToPatch, err, se )
				os.Exit(1)
			}
			Out.Info( "Applying patch [%v]: %v", patch.Patch, fileToPatch)
			// Overwrite the prior file with the patched version
			err = ioutil.WriteFile( fileToPatch, []byte(so), 0600 )
			if err != nil {
				Out.Error("Error writing patch result on %v: %v", fileToPatch, err )
				os.Exit(1)
			}
		}
	}


	//so, se, err = OC.Exec("get", "dc")
	//Out.Out( "%v\n%v\n: %v", so, se, err )

}


func init() {
	RootCmd.AddCommand(exportCmd)
	exportCmd.Flags().StringVar(&cfg.xrFile, "config", "", "Path to ObjectRepository JSON file")
	exportCmd.Flags().StringVar(&cfg.version, "to", "master", "Version to export")

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// exportCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// exportCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

}
