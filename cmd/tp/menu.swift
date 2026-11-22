import AppKit
import CryptoKit

struct Activity: Decodable {
    struct Project: Decodable {
        let name: String
        let doing: Int
        let attention: Int
    }

    let username: String
    let projects: [Project]
}

final class TeleportMenu: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let menu = NSMenu()
    private let settings = UserDefaults.standard
    private let credentialDirectory = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Application Support/Teleport Menu", isDirectory: true)
    private let cli = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/tp")
    private var endpoints: [String] = []
    private var endpoint = ""
    private var activity: Activity?
    private var message = "Sign in to see project activity"
    private var generation = 0
    private var request: Process?
    private var signIn: Process?
    private var signingOut = false
    private var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let initial = CommandLine.arguments.dropFirst().first.flatMap(serverOrigin) ?? "https://teleport.engineering"
        endpoints = settings.stringArray(forKey: "servers") ?? [initial]
        endpoint = settings.string(forKey: "server") ?? initial
        if !endpoints.contains(endpoint) { endpoints.insert(endpoint, at: 0) }
        menu.delegate = self
        menu.autoenablesItems = false
        item.menu = menu
        render()
        let timer = Timer(timeInterval: 30, repeats: true) { [weak self] _ in self?.poll() }
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
        poll()
    }

    private func serverOrigin(_ input: String) -> String? {
        guard var parts = URLComponents(string: input.trimmingCharacters(in: .whitespacesAndNewlines)),
              let host = parts.host?.lowercased(), !host.isEmpty,
              let scheme = parts.scheme?.lowercased(),
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.path.isEmpty || parts.path == "/" else { return nil }
        let loopback = host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
        guard scheme == "https" || (scheme == "http" && loopback) else { return nil }
        parts.scheme = scheme
        parts.host = host
        if (scheme == "https" && parts.port == 443) || (scheme == "http" && parts.port == 80) { parts.port = nil }
        parts.path = ""
        return parts.url?.absoluteString
    }

    private func credentialFile(_ server: String) -> URL {
        let key = SHA256.hash(data: Data(server.utf8)).map { String(format: "%02x", $0) }.joined()
        return credentialDirectory.appendingPathComponent(key + ".json")
    }

    private func run(_ command: [String], at server: String, completion: @escaping (Bool, Data) -> Void) -> Process? {
        let process = Process()
        let output = Pipe()
        process.executableURL = cli
        process.arguments = command + ["-server", server, "-config", credentialFile(server).path, "-json"]
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        process.standardInput = FileHandle.nullDevice
        // Select the endpoint and credential file explicitly even when the
        // surrounding GUI session has exported CLI defaults.
        var environment = ProcessInfo.processInfo.environment
        for key in ["TELEPORT_SERVER", "TELEPORT_CONFIG", "TELEPORT_USERNAME", "TELEPORT_PASSWORD"] {
            environment.removeValue(forKey: key)
        }
        process.environment = environment
        do { try process.run() } catch { return nil }
        DispatchQueue.global(qos: .utility).async {
            let data = output.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            let success = process.terminationStatus == 0
            DispatchQueue.main.async { completion(success, data) }
        }
        return process
    }

    func menuWillOpen(_ menu: NSMenu) {
        render()
        menuIsOpen = true
    }

    private func poll() {
        guard request == nil, !signingOut else { return }
        guard FileManager.default.fileExists(atPath: credentialFile(endpoint).path) else {
            activity = nil
            message = signIn == nil ? "Sign in to see project activity" : "Waiting for browser sign-in…"
            render()
            return
        }
        let revision = generation
        request = run(["api", "getMenuStatus"], at: endpoint) { [weak self] success, data in
            guard let self = self, self.generation == revision else { return }
            self.request = nil
            self.activity = success ? try? JSONDecoder().decode(Activity.self, from: data) : nil
            self.message = self.activity == nil ? "Server unavailable or sign-in expired" : "Connected"
            self.render()
        }
        if request == nil {
            activity = nil
            message = "Could not run the Teleport client"
            render()
        }
    }

    private func color(doing: Int, attention: Int) -> NSColor {
        if attention > 0 { return .systemOrange }
        if doing > 0 { return .systemGreen }
        return .systemBlue
    }

    private func dot(_ color: NSColor, size: CGFloat) -> NSImage {
        let icon = NSImage(size: NSSize(width: size, height: size), flipped: false) { frame in
            color.setFill()
            NSBezierPath(ovalIn: frame.insetBy(dx: 1, dy: 1)).fill()
            return true
        }
        icon.isTemplate = false
        return icon
    }

    private func row(_ title: String, action: Selector? = nil, in target: NSMenu? = nil) -> NSMenuItem {
        let row = NSMenuItem(title: title, action: action, keyEquivalent: "")
        row.target = self
        row.isEnabled = action != nil
        (target ?? menu).addItem(row)
        return row
    }

    private func render() {
        // Avoid rebuilding a menu while AppKit is tracking a selection.
        let projects = activity?.projects ?? []
        let doing = projects.reduce(0) { $0 + $1.doing }
        let attention = projects.reduce(0) { $0 + $1.attention }
        let summary = activity == nil ? message : "Doing \(doing) · Attention \(attention)"
        item.button?.image = dot(activity == nil ? .systemGray : color(doing: doing, attention: attention), size: 14)
        item.button?.toolTip = "Teleport · \(summary)"
        item.button?.setAccessibilityLabel("Teleport · \(summary)")
        if menuIsOpen { return }
        menu.removeAllItems()
        _ = row(endpoint)
        if let activity = activity { _ = row("Signed in as \(activity.username)") }
        _ = row(summary)
        if !projects.isEmpty {
            menu.addItem(.separator())
            for project in projects.prefix(20) {
                let entry = row("\(project.name)  ·  Doing \(project.doing)  ·  Attention \(project.attention)")
                entry.image = dot(color(doing: project.doing, attention: project.attention), size: 10)
            }
            if projects.count > 20 { _ = row("Open Teleport to see all projects") }
        }
        menu.addItem(.separator())
        _ = row("Open Teleport", action: #selector(openWorkspace))
        let submenu = NSMenu(title: "Switch Server")
        submenu.autoenablesItems = false
        for server in endpoints {
            let option = row(server, action: #selector(selectServer(_:)), in: submenu)
            option.representedObject = server
            option.state = server == endpoint ? .on : .off
        }
        let switcher = row("Switch Server")
        switcher.submenu = submenu
        switcher.isEnabled = !signingOut
        row("Settings…", action: #selector(showSettings)).isEnabled = !signingOut
        if signIn != nil {
            _ = row("Cancel Sign-In", action: #selector(cancelSignIn))
        } else if activity == nil {
            _ = row("Sign In…", action: #selector(beginSignIn))
        }
        if FileManager.default.fileExists(atPath: credentialFile(endpoint).path) && signIn == nil && !signingOut {
            _ = row("Sign Out", action: #selector(logout))
        }
        menu.addItem(.separator())
        _ = row("Refresh", action: #selector(refresh))
        _ = row("Quit Teleport Menu", action: #selector(quit))
    }

    private var menuIsOpen = false
    func menuDidClose(_ menu: NSMenu) { menuIsOpen = false; render() }

    @objc private func selectServer(_ sender: NSMenuItem) {
        guard let server = sender.representedObject as? String else { return }
        useServer(server)
    }

    private func invalidateRequests() {
        generation += 1
        if request?.isRunning == true { request?.terminate() }
        request = nil
        if signIn?.isRunning == true { signIn?.terminate() }
        signIn = nil
        signingOut = false
    }

    private func useServer(_ server: String) {
        guard server != endpoint, !signingOut else { return }
        invalidateRequests()
        endpoint = server
        activity = nil
        if !endpoints.contains(server) { endpoints.append(server) }
        settings.set(endpoints, forKey: "servers")
        settings.set(endpoint, forKey: "server")
        message = "Checking Teleport…"
        render()
        poll()
    }

    @objc private func showSettings() {
        let alert = NSAlert()
        alert.messageText = "Teleport Menu Settings"
        alert.informativeText = "Choose or enter an HTTPS server, such as https://teleport.engineering or your Teleport tailnet address."
        let field = NSComboBox(frame: NSRect(x: 0, y: 0, width: 350, height: 28))
        field.addItems(withObjectValues: endpoints)
        field.stringValue = endpoint
        alert.accessoryView = field
        alert.addButton(withTitle: "Use Server")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        while alert.runModal() == .alertFirstButtonReturn {
            if let server = serverOrigin(field.stringValue) { useServer(server); return }
            alert.informativeText = "Enter an HTTPS origin or a local loopback address. Leave off paths, queries and credentials."
        }
    }

    @objc private func beginSignIn() {
        guard signIn == nil, !signingOut else { return }
        let revision = generation
        message = "Waiting for browser sign-in…"
        signIn = run(["login", "-browser"], at: endpoint) { [weak self] success, _ in
            guard let self = self, self.generation == revision else { return }
            self.signIn = nil
            self.message = success ? "Checking Teleport…" : "Sign-in did not complete. Try again."
            self.render()
            if success { self.poll() }
        }
        if signIn == nil { message = "Could not open browser sign-in" }
        render()
    }

    @objc private func cancelSignIn() {
        invalidateRequests()
        message = "Sign-in cancelled"
        render()
    }

    @objc private func logout() {
        invalidateRequests()
        let revision = generation
        signingOut = true
        if run(["logout"], at: endpoint, completion: { [weak self] success, _ in
            guard let self = self, self.generation == revision else { return }
            self.signingOut = false
            self.activity = nil
            self.message = success ? "Signed out" : "Could not sign out from this server"
            self.render()
        }) == nil {
            signingOut = false
            message = "Could not run the Teleport client"
        }
        render()
    }

    @objc private func refresh() { poll() }

    @objc private func openWorkspace() {
        if let url = URL(string: endpoint) { NSWorkspace.shared.open(url) }
    }

    @objc private func quit() {
        invalidateRequests()
        timer?.invalidate()
        NSApp.terminate(nil)
    }
}

let app = NSApplication.shared
let controller = TeleportMenu()
app.setActivationPolicy(.accessory)
app.delegate = controller
app.run()
