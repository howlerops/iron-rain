#if os(iOS)
import OculusUI
import UIKit
import UserNotifications

/// Captures the APNs device token and handles actionable approval notifications.
/// The token is handed to `OculusStore` so the Model registers it on connect
/// (`device.register`); the daemon then pushes tool approvals to this device.
final class PushDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    static let approvalCategory = "APPROVAL"

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self

        let allow = UNNotificationAction(identifier: "ALLOW", title: "Allow", options: [.authenticationRequired])
        let deny = UNNotificationAction(identifier: "DENY", title: "Deny", options: [.destructive, .authenticationRequired])
        let category = UNNotificationCategory(
            identifier: Self.approvalCategory, actions: [allow, deny],
            intentIdentifiers: [], options: []
        )
        center.setNotificationCategories([category])

        center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
            guard granted else { return }
            DispatchQueue.main.async { application.registerForRemoteNotifications() }
        }
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let hex = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { @MainActor in OculusStore.shared.deviceToken = hex }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // Non-fatal: the app still works over a direct/LAN connection without push.
    }

    // Foreground notifications: still show the banner.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

    // Approve/Deny tapped on the lock screen.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let decision: String?
        switch response.actionIdentifier {
        case "ALLOW": decision = "allow" // matches OculusKit Decision.allow
        case "DENY": decision = "deny"   // matches OculusKit Decision.deny
        default: decision = nil
        }
        // Any tap (approval or "agent finished"/"error") that carries a session id opens
        // that session on the next connect — tap the notification, land in the session.
        let sid = response.notification.request.content.userInfo["session_id"] as? String
        Task { @MainActor in
            if let decision { OculusStore.shared.pendingDecision = decision }
            if let sid, decision == nil { OculusStore.shared.handoffSessionID = sid }
        }
        completionHandler()
    }
}
#endif
