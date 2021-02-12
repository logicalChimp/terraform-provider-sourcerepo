package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	// billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceRepo() *schema.Resource {
	return &schema.Resource{
		Description: "The resource `sourecerepo_repo` verifies a repo exists and the supplied credentials work.",
		Create:      ReadRepo,
		Read:        ReadRepo,
		Delete:      schema.RemoveFromState,

		Schema: map[string]*schema.Schema{
			"repo_name": {
				Description: "Name of the repository to connect to",
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
			},

			"init_if_empty": {
				Description: "If the target Source Repo is empty (e.g. newly created), initialise it",
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				ForceNew:    true,
			},

			"trigger": {
				Description: "used to trigger re-execution",
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
			},

			"project": {
				Description: "The GCP Project hosting the repository",
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
			},

			"username": {
				Description: "Name of the user to for authenticating access",
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
			},

			"private_key_pem_bytes_b64": {
				Description: "The base64 encoded Google JSON key (e.g. for a service account)",
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Sensitive:   true,
			},

			"private_key_str": {
				Description: "The private key str for the specified user",
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Sensitive:   true,
			},

			"status": {
				Description: "Output status from 'reading' the repo",
				Type:        schema.TypeString,
				Computed:    true,
			},
		},
	}
}

type google_sa_key struct {
	Project_id                  string
	Type                        string
	Private_key_id              string
	Private_key                 string
	Client_email                string
	Client_id                   string
	Auth_uri                    string
	Token_uri                   string
	Auth_provider_x509_cert_url string
	Client_x509_cert_url        string
}

func ReadRepo(d *schema.ResourceData, meta interface{}) error {
	repo_template := "ssh://source.developers.google.com:2022/p/%s/r/%s"

	project := d.Get("project").(string)
	repo_name := d.Get("repo_name").(string)
	username := d.Get("username").(string)
	repo_path := fmt.Sprintf(repo_template, project, repo_name)

	private_key := ""
	rsaJson := ""
	rsaBytesB64 := d.Get("private_key_pem_bytes_b64").(string)
	if rsaBytesB64 != "" {
		rsaBytes, err := base64.StdEncoding.DecodeString(rsaBytesB64)
		if err != nil {
			return fmt.Errorf("Failed to B64 decode PrivateKey: %s", err)
		}

		rsaJson = bytes.NewBuffer(rsaBytes).String()
		var gkey google_sa_key
		json.Unmarshal([]byte(rsaJson), &gkey)

		// pemKey, _ := pem.Decode([]byte(gkey.Private_key))
		// if pemKey == nil {
		// 	return fmt.Errorf("Failed to PEM decode PrivateKey: %s", gkey.Private_key)
		// }

		private_key = gkey.Private_key
	} else {
		private_key = d.Get("private_key_str").(string)
	}

	auth, err := ssh.NewPublicKeys(username, []byte(private_key), "")
	if err != nil {
		return fmt.Errorf("Failed to create SSH Key for %s: %s", username, err)
	}

	storage := memory.NewStorage()
	fs := memfs.New()
	local, err := git.Init(storage, fs)
	if err != nil {
		d.Set("status", fmt.Sprintf("Failed to init local repo [%s]: %s", repo_path, err))
		d.SetId(repo_path)

		return nil
	}

	_, err = local.CreateRemote(&config.RemoteConfig{
		Name: repo_name,
		URLs: []string{repo_path},
	})
	if err != nil {
		d.Set("status", fmt.Sprintf("Failed to Create Remote, repo [%s]: %s", repo_path, err))
		d.SetId(repo_path)

		return nil
	}

	err = local.Fetch(&git.FetchOptions{
		Auth:       auth,
		RemoteName: repo_name,
	})
	if err != nil {
		if "remote repository is empty" == err.Error() {
			filepath := "Readme.md"
			newfile, err := fs.Create(filepath)
			if err != nil {
				d.Set("status", fmt.Sprintf("Failed to Create placeholder Readme: %s", err))
				d.SetId(repo_path)

				return nil
			}
			newfile.Write([]byte(fmt.Sprintf("# Repo: %s", repo_name)))
			newfile.Close()

			worktree, err := local.Worktree()
			if err != nil {
				d.Set("status", fmt.Sprintf("Failed to retrieve Worktree for [%s]: %s", repo_path, err))
				d.SetId(repo_path)

				return nil
			}
			worktree.Add(filepath)
			worktree.Commit("Initialised with blank Readme", &git.CommitOptions{})

			err = local.Push(&git.PushOptions{
				Auth:       auth,
				RemoteName: repo_name,
			})
			if err != nil {
				d.Set("status", fmt.Sprintf("Failed to Push to bare repo [%s]: %s", repo_path, err))
				d.SetId(repo_path)

				return nil
			} else {
				d.Set("status", "pushed")
				d.SetId(repo_path)

				return nil
			}
		} else {
			d.Set("status", fmt.Sprintf("Failed to Fetch from remote repo [%s]: %T, %s", repo_path, err, err))
			d.SetId(repo_path)

			return nil
		}
	}

	_, err = git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL:  repo_path,
		Auth: auth,
	})
	if err != nil {
		// return fmt.Errorf("Failed to open repo [%s]: %s", repo_path, err)
		d.Set("status", fmt.Sprintf("Failed to clone repo [%s]: %s", repo_path, err))
		d.SetId(repo_path)

		return nil
	}

	d.Set("status", "fetched")
	d.SetId(repo_path)

	return nil
}
