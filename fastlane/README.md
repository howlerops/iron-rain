fastlane documentation
----

# Installation

Make sure you have the latest version of the Xcode command line tools installed:

```sh
xcode-select --install
```

For _fastlane_ installation instructions, see [Installing _fastlane_](https://docs.fastlane.tools/#installing-fastlane)

# Available Actions

## iOS

### ios check

```sh
[bundle exec] fastlane ios check
```

Verify the App Store Connect API key authenticates and whether the app exists.

### ios bootstrap_app

```sh
[bundle exec] fastlane ios bootstrap_app
```

Create the app record in App Store Connect (run once).

### ios beta

```sh
[bundle exec] fastlane ios beta
```

Build the iOS app and upload it to TestFlight (external testers).

### ios beta_internal

```sh
[bundle exec] fastlane ios beta_internal
```

Build + upload without submitting for external review (internal testing only).

----

This README.md is auto-generated and will be re-generated every time [_fastlane_](https://fastlane.tools) is run.

More information about _fastlane_ can be found on [fastlane.tools](https://fastlane.tools).

The documentation of _fastlane_ can be found on [docs.fastlane.tools](https://docs.fastlane.tools).
