#!/bin/bash

# deploy.sh

# Function to display usage
usage() {
    echo "Usage: $0 [-b|--beta] <version>"
    echo "Example for main: $0 v1.0.0"
    echo "Example for beta: $0 -b v1.0.0"
    exit 1
}

# Parse arguments
BETA=false

while [[ "$#" -gt 0 ]]; do
    case $1 in
        -b|--beta) BETA=true; shift ;;
        -*) echo "Unknown parameter: $1"; usage ;;
        *) VERSION="$1"; shift ;;
    esac
done

# Check if version is provided
if [ -z "$VERSION" ]; then
    echo "Error: Version is required"
    usage
fi

# Validate version format
if ! [[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: Version must be in format v1.0.0"
    exit 1
fi

# Set tag based on branch
if [ "$BETA" = true ]; then
    TAG="$VERSION-beta"
    BRANCH="beta"
else
    TAG="$VERSION"
    BRANCH="main"
fi

echo "Deploying version $VERSION to $BRANCH branch..."

# Ensure we're on the right branch
git checkout $BRANCH || exit 1

# Create and push tag
echo "Creating tag $TAG..."
git tag "$TAG" || exit 1
git push origin "$TAG" || exit 1

echo "Deployment initiated successfully!"
echo "GitHub Actions will handle the release process."
echo "Check the progress at: https://github.com/sirrobot01/decypharr/actions"