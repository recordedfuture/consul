package tokenupdate

import (
	"flag"
	"fmt"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/command/acl"
	"github.com/hashicorp/consul/command/flags"
	"github.com/mitchellh/cli"
)

func New(ui cli.Ui) *cmd {
	c := &cmd{UI: ui}
	c.init()
	return c
}

type cmd struct {
	UI    cli.Ui
	flags *flag.FlagSet
	http  *flags.HTTPFlags
	help  string

	tokenID     string
	policyIDs   []string
	policyNames []string
	description string

	mergePolicies bool
}

func (c *cmd) init() {
	c.flags = flag.NewFlagSet("", flag.ContinueOnError)
	c.flags.BoolVar(&c.mergePolicies, "merge-policies", false, "Merge the new policies "+
		"with the existing policies")
	c.flags.StringVar(&c.tokenID, "id", "", "The Accessor ID of the token to read. "+
		"It may be specified as a unique ID prefix but will error if the prefix "+
		"matches multiple token Accessor IDs")
	c.flags.StringVar(&c.description, "description", "", "A description of the token")
	c.flags.Var((*flags.AppendSliceValue)(&c.policyIDs), "policy-id", "ID of a "+
		"policy to use for this token. May be specified multiple times")
	c.flags.Var((*flags.AppendSliceValue)(&c.policyNames), "policy-name", "Name of a "+
		"policy to use for this token. May be specified multiple times")
	c.http = &flags.HTTPFlags{}
	flags.Merge(c.flags, c.http.ClientFlags())
	flags.Merge(c.flags, c.http.ServerFlags())
	c.help = flags.Usage(help, c.flags)
}

func (c *cmd) Run(args []string) int {
	if err := c.flags.Parse(args); err != nil {
		return 1
	}

	if c.tokenID == "" {
		c.UI.Error(fmt.Sprintf("Cannot update a token without specifying the -id parameter"))
		return 1
	}

	client, err := c.http.APIClient()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error connecting to Consul agent: %s", err))
		return 1
	}

	tokenID, err := acl.GetTokenIDFromPartial(client, c.tokenID)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error determining token ID: %v", err))
		return 1
	}

	token, _, err := client.ACL().TokenRead(tokenID, nil)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error when retrieving current token: %v", err))
		return 1
	}

	token.Description = c.description

	if c.mergePolicies {
		for _, policyName := range c.policyNames {
			found := false
			for _, link := range token.Policies {
				if link.Name == policyName {
					found = true
					break
				}
			}

			if !found {
				// We could resolve names to IDs here but there isn't any reason why its would be better
				// than allowing the agent to do it.
				token.Policies = append(token.Policies, &api.ACLTokenPolicyLink{Name: policyName})
			}
		}

		for _, policyID := range c.policyIDs {
			policyID, err := acl.GetPolicyIDFromPartial(client, policyID)
			if err != nil {
				c.UI.Error(fmt.Sprintf("Error resolving policy ID %s: %v", policyID, err))
				return 1
			}
			found := false

			for _, link := range token.Policies {
				if link.ID == policyID {
					found = true
					break
				}
			}

			if !found {
				token.Policies = append(token.Policies, &api.ACLTokenPolicyLink{ID: policyID})
			}
		}
	} else {
		token.Policies = nil

		for _, policyName := range c.policyNames {
			// We could resolve names to IDs here but there isn't any reason why its would be better
			// than allowing the agent to do it.
			token.Policies = append(token.Policies, &api.ACLTokenPolicyLink{Name: policyName})
		}

		for _, policyID := range c.policyIDs {
			policyID, err := acl.GetPolicyIDFromPartial(client, policyID)
			if err != nil {
				c.UI.Error(fmt.Sprintf("Error resolving policy ID %s: %v", policyID, err))
				return 1
			}
			token.Policies = append(token.Policies, &api.ACLTokenPolicyLink{ID: policyID})
		}
	}

	token, _, err = client.ACL().TokenUpdate(token, nil)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to update token %s: %v", tokenID, err))
		return 1
	}

	c.UI.Info("Token updated successfully.")
	acl.PrintToken(token, c.UI, true)
	return 0
}

func (c *cmd) Synopsis() string {
	return synopsis
}

func (c *cmd) Help() string {
	return flags.Usage(c.help, nil)
}

const synopsis = "Update an ACL Token"
const help = `
Usage: consul acl token update [options]

    This command will update a token. Some parts such as marking the token local
    cannot be changed.

    Update a token description and take the policies from the existing token:

        $ consul acl token update -id abcd -description "replication" -merge-policies

      Update all editable fields of the token:

          $ consul acl token update -id abcd -description "replication" -policy-name "token-replication"
`
