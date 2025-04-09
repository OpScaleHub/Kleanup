#!/bin/bash

# Script to configure a GitHub repository to automatically clean up merged feature branches
# using the gh CLI.

# Prerequisites:
# 1. gh CLI installed and authenticated.
# 2. You are in the root directory of a local Git repository that is connected to a GitHub repository.

# Function to check if gh CLI is installed
check_gh_installed() {
  if ! command -v gh &> /dev/null; then
    echo "Error: GitHub CLI (gh) is not installed."
    echo "Please install it from https://cli.github.com/"
    exit 1
  fi
}

# Function to check if the user is logged in to gh
check_gh_logged_in() {
  if ! gh auth status &> /dev/null; then
    echo "Error: You are not logged in to GitHub CLI."
    echo "Please run 'gh auth login' to authenticate."
    exit 1
  fi
}

# Function to get the repository owner and name
get_repo_info() {
  local remote_url=$(git config --get remote.origin.url)
  if [[ -z "$remote_url" ]]; then
    echo "Error: Could not determine the remote origin URL."
    echo "Make sure this repository has a remote named 'origin'."
    exit 1
  fi

  # Extract owner and repo name from the URL
  if [[ "$remote_url" =~ ://[^/]+/([^/]+)/([^.]+)(\.git)?$ ]]; then
    export REPO_OWNER="${BASH_REMATCH[1]}"
    export REPO_NAME="${BASH_REMATCH[2]}"
  elif [[ "$remote_url" =~ git@github\.com:([^/]+)/([^.]+)(\.git)?$ ]]; then
    export REPO_OWNER="${BASH_REMATCH[1]}"
    export REPO_NAME="${BASH_REMATCH[2]}"
  else
    echo "Error: Could not parse the repository owner and name from the remote URL: $remote_url"
    exit 1
  fi
}

# Function to check if the 'Allow auto-merge' setting is enabled
check_auto_merge_enabled() {
  echo "Skipping 'Allow auto-merge' check as the field is not available in the GitHub API."
}

# Function to configure automatic branch deletion after merge
configure_auto_delete() {
  echo "Configuring automatic deletion of merged feature branches for $REPO_OWNER/$REPO_NAME..."
  gh repo edit "$REPO_OWNER/$REPO_NAME" --delete-branch-on-merge
  if [ $? -eq 0 ]; then
    echo "Successfully configured automatic deletion of merged branches."
  else
    echo "Error: Failed to configure automatic deletion of merged branches."
    exit 1
  fi
}

# Main script execution
echo "Checking prerequisites..."
check_gh_installed
check_gh_logged_in

echo "Getting repository information..."
get_repo_info

echo "Checking repository settings..."
check_auto_merge_enabled

echo "Configuring automatic branch deletion..."
configure_auto_delete

echo "Done."
echo "Merged feature branches will now be automatically deleted after being merged into the main branch."