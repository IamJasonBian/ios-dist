// TaildropAck: tells the taildrop install server this app launched. Drop this file into a
// dev app and build it with `ACK=1 taildrop.sh …`. Debug builds only; App Store builds skip it.
//
// Sends GET <server>/api/ack?bundle=&build=&vid= on launch and on every return to the
// foreground. A failed send is simply retried on the next foreground.
#if DEBUG
import UIKit

@MainActor
enum TaildropAck {
    static let server = URL(string: "https://great-lakes.tail70b2c1.ts.net:8443/ota/api/ack")!
    private static var observer: NSObjectProtocol?

    /// Call once at startup, e.g. from the App's init or application(_:didFinishLaunchingWithOptions:).
    static func start() {
        send()
        observer = NotificationCenter.default.addObserver(
            forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main
        ) { _ in MainActor.assumeIsolated { send() } }
    }

    static func send() {
        let info = Bundle.main.infoDictionary ?? [:]
        var c = URLComponents(url: server, resolvingAgainstBaseURL: false)!
        c.queryItems = [
            URLQueryItem(name: "bundle", value: Bundle.main.bundleIdentifier),
            URLQueryItem(name: "build", value: info["CFBundleVersion"] as? String),
            URLQueryItem(name: "vid", value: UIDevice.current.identifierForVendor?.uuidString),
        ]
        var req = URLRequest(url: c.url!, timeoutInterval: 10)
        req.cachePolicy = .reloadIgnoringLocalCacheData
        URLSession.shared.dataTask(with: req).resume()
    }
}
#endif
