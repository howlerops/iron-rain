// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "OculusKit",
    platforms: [.macOS(.v13), .iOS(.v16)],
    products: [
        .library(name: "OculusKit", targets: ["OculusKit"]),
        .library(name: "OculusUI", targets: ["OculusUI"]),
    ],
    targets: [
        .target(name: "OculusKit"),
        .target(
            name: "OculusUI",
            dependencies: ["OculusKit"],
            resources: [.process("Resources")]
        ),
        .testTarget(name: "OculusKitTests", dependencies: ["OculusKit"]),
    ]
)
