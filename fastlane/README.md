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

### ios signing

```sh
[bundle exec] fastlane ios signing
```

Resolve signing WITHOUT building or uploading — proves a build would reuse existing profiles.

### ios bootstrap_app

```sh
[bundle exec] fastlane ios bootstrap_app
```

Register the bundle id + ensure an External Testers group (run once).

### ios archive_check

```sh
[bundle exec] fastlane ios archive_check
```

Validate the release archive builds + signs (no TestFlight) — de-risks `beta`.

### ios beta

```sh
[bundle exec] fastlane ios beta
```

Build the iOS app and upload it to TestFlight (external testers).

### ios distribute

```sh
[bundle exec] fastlane ios distribute
```

Distribute the latest ALREADY-uploaded build to External Testers (no rebuild).

### ios submit_external

```sh
[bundle exec] fastlane ios submit_external
```

Set Test Information + submit the latest build for external beta review (raw ASC REST).

### ios beta_internal

```sh
[bundle exec] fastlane ios beta_internal
```

Build + upload without submitting for external review (internal testing only).

----

This README.md is auto-generated and will be re-generated every time [_fastlane_](https://fastlane.tools) is run.

More information about _fastlane_ can be found on [fastlane.tools](https://fastlane.tools).

The documentation of _fastlane_ can be found on [docs.fastlane.tools](https://docs.fastlane.tools).
