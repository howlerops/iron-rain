// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "OculusKit",
    platforms: [.macOS(.v13), .iOS(.v16)],
    products: [
        .library(name: "OculusKit", targets: ["OculusKit"]),
    ],
    targets: [
        .target(name: "OculusKit"),
        .testTarget(name: "OculusKitTests", dependencies: ["OculusKit"]),
    ]
)
