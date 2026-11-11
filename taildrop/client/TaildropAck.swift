// TaildropAck: tells the install store this app launched, so the store can mark an install
// `installed` and show each app's last launch. Call `TaildropAck.start()` once at startup and
// publish with `ACK=1 taildrop.sh …`. Compiled only into Debug and TAILDROP (Ad Hoc) builds.
//
// Sends GET <store>/api/ack?bundle=&build=&vid= on launch and on every return to the
// foreground; a failed send is retried on the next foreground.
//
// TODO: bundle with distributions — have taildrop.sh compile this into every build it ships
// (e.g. a local Swift package or a build phase that adds this file), so apps get launch
// tracking without code changes and ACK=1 becomes the default.
#if DEBUG || TAILDROP
import UIKit

enum TaildropAck {
    static let store = URL(string: "https://great-lakes.tail70b2c1.ts.net:8443/ota/api/ack")!
    @MainActor private static var lastSent = Date.distantPast

    /// Call once at startup (App init or application(_:didFinishLaunchingWithOptions:)).
    @MainActor static func start() {
        send()
        _ = NotificationCenter.default.addObserver(
            forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main
        ) { _ in MainActor.assumeIsolated { send() } }
    }

    @MainActor static func send() {
        // iOS also posts willEnterForeground right after launch; one ACK per launch is enough.
        guard Date().timeIntervalSince(lastSent) > 5 else { return }
        lastSent = Date()
        // identifierForVendor can be nil right after a device restart; the next foreground retries.
        guard let vid = UIDevice.current.identifierForVendor?.uuidString else { return }
        var url = URLComponents(url: store, resolvingAgainstBaseURL: false)!
        url.queryItems = [
            URLQueryItem(name: "bundle", value: Bundle.main.bundleIdentifier),
            URLQueryItem(name: "build", value: Bundle.main.infoDictionary?["CFBundleVersion"] as? String),
            URLQueryItem(name: "vid", value: vid),
        ]
        URLSession.shared.dataTask(with: URLRequest(url: url.url!, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 10)).resume()
    }
}
#endif
