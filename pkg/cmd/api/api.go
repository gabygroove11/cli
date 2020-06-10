package api

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/cli/cli/pkg/cmdutil"
	"github.com/cli/cli/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ApiOptions struct {
	IO *iostreams.IOStreams

	RequestMethod       string
	RequestMethodPassed bool
	RequestPath         string
	RequestInputFile    string
	MagicFields         []string
	RawFields           []string
	RequestHeaders      []string
	ShowResponseHeaders bool

	HttpClient func() (*http.Client, error)
}

func NewCmdApi(f *cmdutil.Factory, runF func(*ApiOptions) error) *cobra.Command {
	opts := ApiOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "Make an authenticated GitHub API request",
		Long: `Makes an authenticated HTTP request to the GitHub API and prints the response.

The <endpoint> argument should either be a path of a GitHub API v3 endpoint, or
"graphql" to access the GitHub API v4.

The default HTTP request method is "GET" normally and "POST" if any parameters
were added. Override the method with '--method'.

Pass one or more '--raw-field' values in "<key>=<value>" format to add
JSON-encoded string parameters to the POST body.

The '--field' flag behaves like '--raw-field' with magic type conversion based
on the format of the value:

- literal values "true", "false", "null", and integer numbers get converted to
  appropriate JSON types;
- if the value starts with "@", the rest of the value is interpreted as a
  filename to read the value from. Pass "-" to read from standard input.

Raw request body may be passed from the outside via a file specified by '--input'.
Pass "-" to read from standard input. In this mode, parameters specified via
'--field' flags are serialized into URL query parameters.
`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			opts.RequestPath = args[0]
			opts.RequestMethodPassed = c.Flags().Changed("method")

			if runF != nil {
				return runF(&opts)
			}
			return apiRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.RequestMethod, "method", "X", "GET", "The HTTP method for the request")
	cmd.Flags().StringArrayVarP(&opts.MagicFields, "field", "F", nil, "Add a parameter of inferred type")
	cmd.Flags().StringArrayVarP(&opts.RawFields, "raw-field", "f", nil, "Add a string parameter")
	cmd.Flags().StringArrayVarP(&opts.RequestHeaders, "header", "H", nil, "Add an additional HTTP request header")
	cmd.Flags().BoolVarP(&opts.ShowResponseHeaders, "include", "i", false, "Include HTTP response headers in the output")
	cmd.Flags().StringVar(&opts.RequestInputFile, "input", "", "The file to use as body for the HTTP request")
	return cmd
}

func apiRun(opts *ApiOptions) error {
	params, err := parseFields(opts)
	if err != nil {
		return err
	}

	method := opts.RequestMethod
	requestPath := opts.RequestPath
	requestHeaders := opts.RequestHeaders
	var requestBody interface{} = params

	if !opts.RequestMethodPassed && (len(params) > 0 || opts.RequestInputFile != "") {
		method = "POST"
	}

	if opts.RequestInputFile != "" {
		file, err := openUserFile(opts.RequestInputFile, opts.IO.In)
		if err != nil {
			return err
		}
		defer file.Close()
		requestPath = addQuery(requestPath, params)
		requestBody = file
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	resp, err := httpRequest(httpClient, method, requestPath, requestBody, requestHeaders)
	if err != nil {
		return err
	}

	if opts.ShowResponseHeaders {
		for name, vals := range resp.Header {
			fmt.Fprintf(opts.IO.Out, "%s: %s\r\n", name, strings.Join(vals, ", "))
		}
		fmt.Fprint(opts.IO.Out, "\r\n")
	}

	if resp.StatusCode == 204 {
		return nil
	}
	defer resp.Body.Close()

	_, err = io.Copy(opts.IO.Out, resp.Body)
	if err != nil {
		return err
	}

	// TODO: detect GraphQL errors
	if resp.StatusCode > 299 {
		return cmdutil.SilentError
	}

	return nil
}

func parseFields(opts *ApiOptions) (map[string]interface{}, error) {
	params := make(map[string]interface{})
	for _, f := range opts.RawFields {
		key, value, err := parseField(f)
		if err != nil {
			return params, err
		}
		params[key] = value
	}
	for _, f := range opts.MagicFields {
		key, strValue, err := parseField(f)
		if err != nil {
			return params, err
		}
		value, err := magicFieldValue(strValue, opts.IO.In)
		if err != nil {
			return params, fmt.Errorf("error parsing %q value: %w", key, err)
		}
		params[key] = value
	}
	return params, nil
}

func parseField(f string) (string, string, error) {
	idx := strings.IndexRune(f, '=')
	if idx == -1 {
		return f, "", fmt.Errorf("field %q requires a value separated by an '=' sign", f)
	}
	return f[0:idx], f[idx+1:], nil
}

func magicFieldValue(v string, stdin io.ReadCloser) (interface{}, error) {
	if strings.HasPrefix(v, "@") {
		return readUserFile(v[1:], stdin)
	}

	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
	}

	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	default:
		return v, nil
	}
}

func readUserFile(fn string, stdin io.ReadCloser) ([]byte, error) {
	r, err := openUserFile(fn, stdin)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ioutil.ReadAll(r)
}

func openUserFile(fn string, stdin io.ReadCloser) (io.ReadCloser, error) {
	if fn == "-" {
		return stdin, nil
	}
	return os.Open(fn)
}
