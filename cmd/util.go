package cmd

import (
	"fmt"
	"os"
	"io"
	"time"
	"strings"
	"regexp"
	"os/exec"
	"bytes"
	"path/filepath"
	"encoding/json"
	"io/ioutil"
)

type Output struct {
}

var Out Output

func (o *Output) toWriter( w io.Writer, format string, vals... interface{} ) {
	fmt.Fprintf( w, format, vals... )
	if ! strings.HasSuffix( format, "\n" ) {
		fmt.Fprintf( os.Stderr, "\n" )
	}
}


func (o *Output) Debug( format string, vals... interface{} ) {
	format = "Debug: " + format
	o.toWriter( os.Stderr, format, vals... )
}

func (o *Output) Error( format string, vals... interface{} ) {
	o.toWriter( os.Stderr, format, vals... )
}

func (o *Output) Warn( format string, vals... interface{} ) {
	format = "Warning: " + format
	o.toWriter( os.Stderr, format, vals... )
}

func (o *Output) Info( format string, vals... interface{} ) {
	o.toWriter( os.Stdout, format, vals... )
}

func (o *Output) Out( format string, vals... interface{} ) {
	o.toWriter( os.Stdout, format, vals... )
}

var endsWithVowelThenY = regexp.MustCompile( ".*[aeiou]y$" )

func pluralizeKind( kind string ) string {
	kind = strings.TrimSpace( kind )
	kind = strings.ToLower( kind )

	if kind == "" {
		return kind
	}

	switch kind {
	case "dc" : kind = "deploymentconfigs"
	case "bc" : kind = "buildconfigs"
	case "svc" : kind = "services"
	case "is" : kind = "imagestreams"
	case "rc" : kind = "replicationcontrollers"
	}

	if strings.HasSuffix( kind, "s" ) {
		return kind
	}
	if strings.HasSuffix( kind, "y" ) == false || endsWithVowelThenY.MatchString(kind) {
		return kind + "s"
	}
	return kind + "ies"
}

// Normalizes kind or kind/name strings to lowercase, pluralized form
func NormalizeType( res string ) string {
	res = strings.TrimSpace( res )
	res = strings.ToLower( res )

	if res == "" {
		return ""
	}

	components := strings.Split( res, "/" )
	components[0] = pluralizeKind( components[0])
	return strings.Join( components, "/" )
}

// Converts an XR kind/name list string to an array of strings
// with full kind name.
func ToKindNameList( list string ) ([]string) {
	var arr []string
	for _,entry := range strings.Split(list, ",") {
		// Normalize the list.. lowercase, pluralized full kind name
		entry = NormalizeType( entry )
		arr = append( arr, entry)
	}
	return arr
}

func GetFullObjectNameFromPath( filename string ) string {
	kindDir, name := filepath.Split( filename )
	name = strings.TrimSuffix( name, ".json" )
	name = strings.TrimSuffix( name, ".yaml" )
	kind := filepath.Base( kindDir )
	return strings.Join( []string{ kind, name }, "/" )
}

func FindAllKindFiles( baseDir string ) ([]string) {
	var fileList []string

	filepath.Walk( baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			Out.Warn( "Unable to walk path [%v]: %v", err, path )
			return nil
		}
		if ! info.Mode().IsRegular() {
			return nil
		}
		fileList = append( fileList, path )
		return nil
	})

	return fileList
}

// Finds all files in a base directory matching a kind/name list.
// Returns a list of filenames.
func FindKindNameFiles( baseDir string, list string ) ([]string) {
	var fileList []string
	for _, i := range ToKindNameList( list ) {

		if i == "all" {
			return FindAllKindFiles( baseDir )
		}

		resPath := filepath.Join( append( []string{ baseDir }, strings.Split( i, "/" )... )... )
		f, err := os.Open( resPath )
		if err != nil {
			continue
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			continue
		}

		if fi.Mode().IsRegular() {
			fileList = append( fileList, resPath )
			continue
		}

		if fi.Mode().IsDir() {
			filepath.Walk( resPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					Out.Warn( "Unable to walk path [%v]: %v", err, path )
					return nil
				}
				if ! info.Mode().IsRegular() {
					return nil
				}
				fileList = append( fileList, path )
				return nil
			})
		}
	}
	return fileList
}

// Looks for any live objects matching entries in a kind[/name] list.
// Returns a map with fully qualified names of live objects as keys.
func FindLiveKindNameMap( kindNameList string ) map[string]struct{} {
	m := make( map[string]struct{} )
	for _, i := range ToKindNameList( kindNameList ) {
		newlineNames, _, err := OC.Exec("get", i, "-o=name")
		if err != nil {
			continue
		}
		for _, objName := range strings.Split( newlineNames, "\n" ) {
			objName = NormalizeType(objName)
			if objName == "" {
				continue
			}
			m[ objName ] = struct{}{}
		}
	}
	return m
}



func Exec( command string, args... string) (string, string, error) {
	cmd := exec.Command( command, args...)
	var stdErrBuff, stdOutBuff bytes.Buffer
	cmd.Stdout = &stdOutBuff
	cmd.Stderr = &stdErrBuff
	err := cmd.Run()
	stdOutBytes := stdOutBuff.Bytes()
	stdErrBytes := stdErrBuff.Bytes()
	stdOut := strings.TrimSpace(string(stdOutBytes))
	stdErr := strings.TrimSpace(string(stdErrBytes))
	switch err.(type) {
	case nil:
		return stdOut, stdErr, nil
	case *exec.ExitError:
		return stdOut, stdErr, err
	default:
		return "", "", nil
	}
}

type OpenShiftCmd struct {
}
var OC OpenShiftCmd
func (oc *OpenShiftCmd) Exec( args... string) (string, string, error) {
	return Exec( "oc", args...)
}

func (oc *OpenShiftCmd) Project() (string, error) {
	projectName, se, err := oc.Exec("project", "-q")
	if err != nil {
		return "", fmt.Errorf( "[%v]: %v", err, se )
	}
	return projectName, nil
}

type GitCmd struct {
	repoDir string
	objectDir string
}

func (git *GitCmd) Exec( args... string) (string, string, error) {
	cd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf( "Unable to acquire current directory: %v", err )
	}
	os.Chdir( git.repoDir )
	defer os.Chdir( cd )
	return Exec( "git", args...)
}

func mapDockerComponent( existing string, mapping *string ) string {
	if mapping == nil { // if mapping was set to null or not specified in JSON
		return existing
	}
	// Setting to "" in JSON instructs code to drop the image reference component.
	// Any other value replaces the image component
	return *mapping
}

func mapDockerComponentWithSuffix( existing string, mapping *string, suffix string ) string {
	v := mapDockerComponent( existing, mapping )
	if v != "" && ! strings.HasSuffix( v, suffix ) {
		v += suffix
	}
	return v
}

func mapDockerTagComponentWithPrefix( existing string, mapping *string ) string {
	v := mapDockerComponent( existing, mapping )
	if v != "" && ! strings.HasPrefix( v, "@" ) && ! strings.HasPrefix( v, ":" ) {
		v = ":" + v
	}
	return v
}

func makeTimestamp() int64 {
    return time.Now().UnixNano() / (int64(time.Millisecond)/int64(time.Nanosecond))
}

func dockerPatternMatches( imageRef, pattern, internalRegistryIP, projectName string ) (bool, error) {
	pRegistry, pNamespace, pRepo, pTag, err := ParseDockerImageRef( pattern )
	if err != nil {
		return false, fmt.Errorf( "Invalid image pattern (%v): %v", pattern, err )
	}

	registry, namespace, repo, tag, err := ParseDockerImageRef( imageRef )
	if err != nil {
		return false, fmt.Errorf( "Invalid image reference (%v): %v", pattern, err )
	}

	if !( pRegistry == "*/" || ( pRegistry == "~/" && strings.HasPrefix( registry, internalRegistryIP ) ) || pRegistry == registry ) {
		Out.Debug( "ImageMapping pattern (%v) does not match registry host (%v) of reference: %v", pattern, registry, imageRef )
		return false, nil
	}

	if !( pNamespace == "*/" || ( pNamespace == "~/" && namespace == projectName ) || pNamespace == namespace ) {
		Out.Debug( "ImageMapping pattern (%v) does not match namespace (%v) of reference: %v", pattern, namespace, imageRef )
		return false, nil
	}

	if !( pRepo == "*" || pRepo == repo ) {
		Out.Debug( "ImageMapping pattern (%v) does not match repository (%v) of reference: %v", pattern, repo, imageRef )
		return false, nil
	}

	if !( pTag == ":*" || pTag == tag ) {
		Out.Debug( "ImageMapping pattern (%v) does not match tag (%v) of reference: %v", pattern, tag, imageRef )
		return false, nil
	}

	return true, nil
}

func ParseDockerImageRef( ref string ) (registry, namespace, repo, tag string, err error){
	components := strings.Split( ref, "/" )

	switch len( components ) {
	case 3:
		registry = components[0] + "/"
		namespace = components[1] + "/"
		repo = components[2]
	case 2:
		namespace = components[0] + "/"
		repo = components[1]
		if strings.ContainsAny( namespace, ":." ) {
			registry = namespace
			namespace = ""
		}
	case 1:
		repo = components[0]
	default:
		fmt.Errorf( "Invalid docker image reference: %v", ref )
		os.Exit(1)
	}

	// Repo still potentially contains @sha256 or normal tag
	digestSplit := strings.Split( repo, "@" )
	if len( digestSplit ) > 1 {
		repo = digestSplit[0]
		tag = "@" + digestSplit[1]
	} else {
		tagSplit := strings.Split( repo, ":" )
		if len( tagSplit ) > 1 {
			repo = tagSplit[0]
			tag = ":" + tagSplit[1]
		}
	}
	return
}

const (
	KIND_RC = "replicationcontrollers"
	KIND_DC = "deploymentconfigs"
	KIND_BC = "buildconfigs"
	KIND_IS = "imagestreams"

	LABEL_REPOSITORY = "openshift.io/repository"
	LABEL_REPOSITORY_VERSION = "openshift.io/repository/version"
)

func GetJSONPath( from interface{}, names ...string ) interface{} {
	for _, name := range names {
		if from == nil {
			return nil
		}
		obj := from.(map[string]interface{})
		from = obj[ name ]
	}
	return from
}

func SetJSONPath( from interface{}, names []string, val interface{} ) {
	if len(names) > 1 {
		for _, name := range names[:len(names)-1] {
			if from == nil {
				return
			}
			obj := from.(map[string]interface{})
			from = obj[ name ]
		}
	}

	if len( names ) > 0 {
		obj := from.(map[string]interface{})
		obj[ names[ len(names)-1 ] ] = val
	}
}

func SetJSONObj( from interface{}, name string, val interface{} ) {
	SetJSONPath( from, []string{ name }, val )
}

func ReadXR( filename string ) (*XR, error) {
	xrString, err := ioutil.ReadFile( filename )

	if err != nil {
		return nil, fmt.Errorf( "Unable to read XR file (%v): %v", exportConfig.xrFile, err )
	}

	var xr XR
	err = json.Unmarshal(xrString, &xr)
	if err != nil {
		return nil, fmt.Errorf( "Error parsing XR file (%v): %v", exportConfig.xrFile, err )
	}

	if xr.Spec.Type != "git" || xr.Spec.Git.Format != "json" {
		return nil, fmt.Errorf( "Only git/json ObjectRepositories are presently supported")
	}

	if xr.Spec.Git.URI == "" {
		return nil, fmt.Errorf( "No Git URI specified")
	}

	return &xr, nil
}

func PrepGitDir( xr *XR ) (*GitCmd, error) {
	gitDir, err := ioutil.TempDir("", "xrgit")

	if err != nil {
		return nil, fmt.Errorf( "Error creating temporary directory for git operations: %v", err )
	}

	git := GitCmd{ repoDir : gitDir }

	Out.Info( "Cloning %v", xr.Spec.Git.URI )
	_,se,err := git.Exec( "clone", "--", xr.Spec.Git.URI, gitDir )

	if err != nil {
		defer os.RemoveAll( gitDir )
		return nil, fmt.Errorf( "Error cloning git repository [%v]: %v", err, se )
	}

	if xr.Spec.Git.Branch.BaseRef == "" {
		xr.Spec.Git.Branch.BaseRef = "master"
	}

	_,se,err = git.Exec( "checkout", xr.Spec.Git.Branch.BaseRef  )

	if err != nil {
		defer os.RemoveAll( gitDir )
		return nil, fmt.Errorf( "Error setting up git repository; does not contain baseRef (%v) [%v]: %v", xr.Spec.Git.Branch.BaseRef, err, se )
	}

	git.objectDir = git.repoDir
	if xr.Spec.Git.Branch.ContextDir != "" {
		git.objectDir = filepath.Join( git.repoDir, xr.Spec.Git.Branch.ContextDir )
		os.MkdirAll( git.objectDir, 0600 )
	}

	return &git, nil
}

// Allows a caller to visit each element of a JSON array.
// The elements the visitor returns will be collected and
// returned from the main method as an interface{} of
// underlying []interface{} .
func VisitJSONArrayElements( from interface{}, arrayWalk func( entry interface{} ) (interface{}) ) (interface{}) {
	arr := from.([]interface{})
	var nArr []interface{}
	for _,i := range arr {
		v := arrayWalk( i )
		if v != nil {
			nArr = append( nArr, v )
		}
	}
	return interface{}(nArr)
}

func SetLabel( in interface{}, key string, val string ) {
	metadata := GetJSONPath( in, "metadata" )
	labels := GetJSONPath( metadata, "labels" )
	if labels == nil {
		labels = make( map[string]interface{} )
	}
	SetJSONObj( labels, key, val )
	SetJSONObj( metadata, "labels", labels )
}

func SetAnnotation( in interface{}, key string, val string ) {
	metadata := GetJSONPath( in, "metadata" )
	annotations := GetJSONPath( metadata, "annotations" )
	if annotations == nil {
		annotations = make( map[string]interface{} )
	}
	SetJSONObj( annotations, key, val )
	SetJSONObj( metadata, "annotations", annotations )
}

func RunPatches( xr *XR, baseDir string ) error {
	for _, patch := range xr.Spec.ExportRules.Transforms.Patches {
		if patch.Type != "jq" {
			return fmt.Errorf( "Patch type is not supported: %v", patch.Type )
		}
		for _,fileToPatch := range FindKindNameFiles( baseDir, patch.Match ) {
			so, se, err := Exec( "jq", patch.Patch, fileToPatch )
			if err != nil {
				return fmt.Errorf( "Error running jq patch operation on %v [%v]: %v", fileToPatch, err, se )
			}
			Out.Info( "Applying patch [%v]: %v", patch.Patch, fileToPatch)
			// Overwrite the prior file with the patched version
			err = ioutil.WriteFile( fileToPatch, []byte(so), 0600 )
			if err != nil {
				return fmt.Errorf("Error writing patch result on %v: %v", fileToPatch, err )
			}
		}
	}
	return nil
}

func IsSelectedByKindNameList( fullResName, list string ) bool {
	for _, entry := range ToKindNameList( list ) {
		if entry == "all" || fullResName == entry || strings.HasPrefix( fullResName, "entry"+"/" ) {
			return true
		}
	}
	return false
}

type Template struct {
	Kind string `json:"kind"`
	Objects []interface{} `json:"objects"`
}

// Converter: https://mholt.github.io/json-to-go/
type XR struct {
	Kind string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Type string `json:"type"`
		DefaultVersion string `json:"defaultVersion"`
		Git struct {
			URI string `json:"uri"`
			Format string `json:"format"`
			Branch struct {
				ContextDir string `json:"contextDir"`
				Prefix string `json:"prefix"`
				BaseRef string `json:"baseRef"`
			} `json:"branch"`
		} `json:"git"`
		ExportRules struct {
			Selectors []struct {
				Namespace string `json:"namespace"`
				MatchLabels []string `json:"matchLabels"`
				MatchExpressions []interface{} `json:"matchExpressions"`
			} `json:"selectors"`
			Include string `json:"include"`
			Exclude string `json:"exclude"`
			Transforms struct {
				PreserveMutators string `json:"preserveMutators"`
			   	Patches []struct {
					Match string `json:"match"`
					Patch string `json:"patch"`
					Type string `json:"type"`
				} `json:"patches"`
				ImageMappings []struct {
					Pattern string `json:"pattern"`
					NewRegistryHost *string `json:"newRegistryHost"`
					NewNamespace *string `json:"newNamespace"`
					NewRepository *string `json:"newRepository"`
					NewTag *string `json:"newTag"`
					Push bool `json:"push"`
					TagType string `json:"tagType"`
				} `json:"imageMappings"`
			} `json:"transforms"`
		} `json:"exportRules"`
		ImportRules struct {
			Include string `json:"include"`
			Exclude string `json:"exclude"`
			Namespace string `json:"namespace"`
			Transforms struct {
				NamePrefix string `json:"namePrefix"`
				Patches []struct {
					Match string `json:"match"`
					Patch string `json:"patch"`
					Type string `json:"type"`
				} `json:"patches"`
				ImageMappings []struct {
					Pattern string `json:"pattern"`
					NewRegistryHost *string `json:"newRegistryHost"`
					NewNamespace *string `json:"newNamespace"`
					NewRepository *string `json:"newRepository"`
					NewTag *string `json:"newTag"`
					Pull bool `json:"pull"`
					TagType string `json:"tagType"`
				} `json:"imageMappings"`
			} `json:"transforms"`
		} `json:"importRules"`
	} `json:"spec"`
}