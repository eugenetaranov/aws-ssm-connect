# aws-ssm-connect

CLI tool for connecting to AWS EC2 instances via SSM Session Manager with fuzzy instance selection.

## Installation

```bash
# Homebrew
brew install eugenetaranov/tap/aws-ssm-connect

# Script
curl -fsSL https://raw.githubusercontent.com/eugenetaranov/aws-ssm-connect/main/install.sh | bash

# From source
make build && sudo make install
```

Requires [session-manager-plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html):
```bash
brew install session-manager-plugin
```

## Usage

```bash
# Interactive fuzzy selection
aws-ssm-connect

# Filter by name
aws-ssm-connect prod-web

# List instances
aws-ssm-connect -l
aws-ssm-connect -l prod web    # filter by multiple words

# Copy files
aws-ssm-connect -copy local.txt i-abc123:/tmp/remote.txt    # upload
aws-ssm-connect -copy i-abc123:/tmp/remote.txt local.txt    # download

# Run command
aws-ssm-connect -run i-abc123 "ls -la /tmp"

# Options
aws-ssm-connect --profile myprofile --region us-west-2
aws-ssm-connect -d  # debug mode
```

## Requirements

- AWS credentials configured
- EC2 instances with SSM Agent installed

## License

MIT
