// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "OculusApp",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(path: "../OculusKit"),
    ],
    targets: [
        .executableTarget(
            name: "OculusApp",
            dependencies: [.product(name: "OculusUI", package: "OculusKit")]
        ),
    ]
)
