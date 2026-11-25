using Gtk;
using Singularity.Widgets;
using Singularity.Accounts;

namespace Sinty.It {

    public class ItWindow : Singularity.Widgets.Window {
        private Gtk.Application app;
        private AppSidebar sidebar;
        private SidebarRow devices_row;
        private SidebarRow requests_row;
        private SidebarRow activity_row;
        private Stack stack;
        private StatusPage failure_page;
        private Box devices_box;
        private Box device_box;
        private Box requests_box;
        private Box activity_box;
        private Fleet? fleet = null;
        private string section = "devices";
        private string? shown_device = null;
        private bool busy = false;

        public ItWindow(Gtk.Application app) {
            Object(application: app);
            this.app = app;
            set_default_size(1000, 700);
            set_title(_("Sinty IT"));

            sidebar = new AppSidebar(220);
            sidebar.box.append(new SidebarSectionLabel(_("Organization")));
            devices_row = section_row("computer-symbolic", _("Devices"), "devices");
            requests_row = section_row("emblem-important-symbolic", _("Requests"), "requests");
            activity_row = section_row("document-open-recent-symbolic", _("Activity"), "activity");
            set_sidebar(sidebar);
            set_sidebar_visible(true);
            add_bubble_icon("view-refresh-symbolic", _("Refresh"), () => refresh());

            stack = new Stack();
            stack.transition_type = StackTransitionType.CROSSFADE;
            stack.add_named(state_page(_("Add your organization account in Settings to manage its devices."),
                "list-add-symbolic", _("Add Organization Account"), _("Settings, Online Accounts, Organization"), open_accounts), "signed-out");
            stack.add_named(state_page(_("Managing devices needs a recent confirmation on one of your devices."),
                "dialog-password-symbolic", _("Confirm on a Device"), _("Sign in again with your organization"), () => step_up()), "stronger");
            stack.add_named(state_page(_("This account does not manage devices. Ask an administrator of your organization to add you."),
                "avatar-default-symbolic", _("Open Online Accounts"), _("Use a different organization account"), open_accounts), "refused");
            var loading = new StatusPage();
            loading.title = _("Loading");
            var spinner = new Spinner();
            spinner.spinning = true;
            loading.child = spinner;
            stack.add_named(loading, "loading");
            failure_page = new StatusPage();
            failure_page.icon_name = "network-error-symbolic";
            failure_page.title = _("Cannot Reach Sinty IT");
            var retry = new Button.with_label(_("Try Again"));
            retry.halign = Align.CENTER;
            retry.add_css_class("suggested-action");
            retry.clicked.connect(() => refresh());
            failure_page.child = retry;
            stack.add_named(failure_page, "failure");
            devices_box = page("devices");
            device_box = page("device");
            requests_box = page("requests");
            activity_box = page("activity");
            set_content(stack);
            stack.visible_child_name = "loading";

            var refresh_action = new SimpleAction("refresh", null);
            refresh_action.activate.connect(() => refresh());
            add_action(refresh_action);
            var section_action = new SimpleAction("section", VariantType.STRING);
            section_action.activate.connect((p) => {
                section = p.get_string();
                shown_device = null;
                refresh();
            });
            add_action(section_action);
            var device_action = new SimpleAction("device", VariantType.STRING);
            device_action.activate.connect((p) => {
                shown_device = p.get_string();
                refresh();
            });
            add_action(device_action);
            var confirm_action = new SimpleAction("confirm-on-device", null);
            confirm_action.activate.connect(() => step_up());
            add_action(confirm_action);
            app.set_accels_for_action("win.refresh", { "<Control>r", "F5" });
            app.set_accels_for_action("win.section::requests", { "<Alt>2" });
            app.set_accels_for_action("win.section::activity", { "<Alt>3" });
            app.set_accels_for_action("win.section::devices", { "<Alt>1", "<Alt>Left" });

            var manager = Manager.get_default();
            manager.account_added.connect(() => refresh());
            manager.account_changed.connect(() => refresh());
            manager.account_removed.connect(() => refresh());
            manager.load.begin((obj, res) => {
                manager.load.end(res);
                refresh();
            });
        }

        private SidebarRow section_row(string icon, string label, string name) {
            var row = new SidebarRow(icon, label);
            row.clicked.connect(() => {
                section = name;
                shown_device = null;
                refresh();
            });
            sidebar.box.append(row);
            return row;
        }

        private WelcomePage state_page(string subtitle, string icon, string action, string detail, owned WelcomePage.ActionCallback callback) {
            var w = new WelcomePage();
            w.app_icon_name = "dev.sinty.IT";
            w.title = _("Sinty IT");
            w.subtitle = subtitle;
            w.is_state = true;
            w.add_action(icon, action, detail, (owned) callback);
            return w;
        }

        private Box page(string name) {
            var box = new PreferencesPage();
            apply_bubble_inset(box);
            var scroll = new ScrolledWindow();
            scroll.hscrollbar_policy = PolicyType.NEVER;
            scroll.child = box;
            stack.add_named(scroll, name);
            return box;
        }

        private static void clear(Box box) {
            Widget? child;
            while ((child = box.get_first_child()) != null) box.remove(child);
        }

        private void open_accounts() {
            Bus.get_proxy.begin<Singularity.Shell.ShellService>(BusType.SESSION, "dev.sinty.desktop", "/dev/sinty/Shell",
                                                                DBusProxyFlags.DO_NOT_AUTO_START, null, (o, r) => {
                try {
                    Bus.get_proxy.end<Singularity.Shell.ShellService>(r).open_settings("accounts");
                } catch (Error e) {
                    toast(_("Open Settings, Online Accounts, to add your organization."));
                }
            });
        }

        private Fleet? pick() {
            foreach (var account in Manager.get_default().get_accounts_for(Capability.DEVICES)) {
                string? url = Fleet.address_of(account);
                if (url == null) continue;
                if (fleet != null && fleet.account == account && fleet.base_url == url) return fleet;
                return new Fleet(account, url);
            }
            return null;
        }

        public void refresh() {
            fleet = pick();
            devices_row.set_active(section == "devices");
            requests_row.set_active(section == "requests");
            activity_row.set_active(section == "activity");
            if (fleet == null) {
                stack.visible_child_name = "signed-out";
                return;
            }
            if (busy) return;
            busy = true;
            load.begin(fleet, (obj, res) => {
                try {
                    load.end(res);
                } catch (FleetError.STRONGER e) {
                    stack.visible_child_name = "stronger";
                } catch (FleetError.REFUSED e) {
                    stack.visible_child_name = "refused";
                } catch (AccountsError.NEEDS_REAUTH e) {
                    stack.visible_child_name = "signed-out";
                } catch (Error e) {
                    failure_page.description = e.message;
                    stack.visible_child_name = "failure";
                }
                busy = false;
            });
        }

        private async void load(Fleet f) throws Error {
            if (f.me == "") yield f.load_me();
            var waiting = yield f.waiting();
            requests_row.badge = waiting.size > 0 ? waiting.size.to_string() : "";
            if (shown_device != null) {
                var commands = new Gee.ArrayList<Command>();
                var d = yield f.device(shown_device, commands);
                show_device(d, commands);
                return;
            }
            switch (section) {
                case "requests": show_requests(waiting); break;
                case "activity": show_activity(yield f.activity(names), names); break;
                default: show_devices(yield f.devices()); break;
            }
        }

        private HashTable<string, string> names = new HashTable<string, string>(str_hash, str_equal);

        private void show_devices(Gee.List<Device> list) {
            clear(devices_box);
            if (list.size == 0) {
                var empty = new StatusPage();
                empty.icon_name = "computer-symbolic";
                empty.title = _("No Devices Yet");
                empty.description = _("Create an enrollment code in the Sinty IT console, then enroll each device from Settings, About, Organization.");
                devices_box.append(empty);
                stack.visible_child_name = "devices";
                return;
            }
            int problems = 0;
            foreach (var d in list) if (d.issues.length > 0) problems++;
            var attention = new PreferencesGroup(_("Need Attention"));
            var fine = new PreferencesGroup(problems > 0 ? _("Other Devices") : null);
            foreach (var d in list) {
                names.insert(d.id, d.name);
                string sub = d.issues.length > 0 ? problem_list(d.issues) : describe(d);
                var row = new ActionRow(d.name, sub, d.issues.length > 0 ? "dialog-warning-symbolic" : "computer-symbolic");
                var seen = new Label(ago(d.seen));
                seen.add_css_class("dim-label");
                row.add_suffix(seen);
                row.add_suffix(new Image.from_icon_name("go-next-symbolic"));
                row.activatable = true;
                string id = d.id;
                row.activated.connect(() => {
                    shown_device = id;
                    refresh();
                });
                if (d.issues.length > 0) {
                    attention.add_row(row);
                } else {
                    fine.add_row(row);
                }
            }
            if (problems > 0) devices_box.append(attention);
            if (problems < list.size) devices_box.append(fine);
            stack.visible_child_name = "devices";
        }

        private void show_device(Device d, Gee.List<Command> commands) {
            clear(device_box);
            var head = new PreferencesGroup(d.name, describe(d));
            head.add_row(new ActionRow(_("Last Seen"), ago(d.seen), "network-transmit-receive-symbolic"));
            if (d.last_user != "") head.add_row(new ActionRow(_("Last Signed In"), d.last_user, "avatar-default-symbolic"));
            head.add_row(new ActionRow(_("Group"), d.group, "folder-symbolic"));
            if (d.disk_free > 0) head.add_row(new ActionRow(_("Free Space"), format_size((uint64) d.disk_free), "drive-harddisk-symbolic"));
            device_box.append(head);

            var safety = new PreferencesGroup(_("Protection"));
            safety.add_row(check_row(_("Disk Encryption"), d.encrypted, _("On"), _("Off: data can be read from the drive")));
            safety.add_row(check_row(_("Keys Sealed to This Hardware"), d.sealed, _("Yes"), _("No: the drive unlocks elsewhere")));
            safety.add_row(check_row(_("Firewall"), d.firewall, _("On"), _("Off")));
            if (d.boot_health != "") safety.add_row(new ActionRow(_("Updates"), update_label(d.boot_health), "software-update-available-symbolic"));
            device_box.append(safety);

            var actions = new PreferencesGroup(_("Actions"), _("The device carries these out the next time it checks in."));
            string[,] kinds = {
                { "lock", "system-lock-screen-symbolic" },
                { "logout", "system-log-out-symbolic" },
                { "reboot", "system-reboot-symbolic" },
                { "update", "software-update-available-symbolic" },
                { "sync-policy", "emblem-synchronizing-symbolic" }
            };
            for (int i = 0; i < kinds.length[0]; i++) {
                string kind = kinds[i, 0];
                var row = new ActionRow(kind_label(kind), null, kinds[i, 1]);
                var go = new Button.with_label(_("Send"));
                go.valign = Align.CENTER;
                go.clicked.connect(() => act("send", d.id, kind, _("%s sent to %s").printf(kind_label(kind), d.name)));
                row.add_suffix(go);
                actions.add_row(row);
            }
            device_box.append(actions);

            var danger = new PreferencesGroup(_("Erase and Remove"), _("Erasing needs a second administrator to approve it."));
            var wipe = new ActionRow(_("Erase"), _("Deletes everything on the device and resets it"), "edit-delete-symbolic");
            var wipe_btn = new Button.with_label(_("Erase…"));
            wipe_btn.valign = Align.CENTER;
            wipe_btn.add_css_class("destructive-action");
            wipe_btn.clicked.connect(() => confirm(_("Erase %s?").printf(d.name),
                _("Everything on %s is deleted once another administrator approves.").printf(d.name), _("Erase"),
                () => act("send", d.id, "wipe", _("Erasing %s waits for a second administrator").printf(d.name))));
            wipe.add_suffix(wipe_btn);
            danger.add_row(wipe);
            var retire = new ActionRow(_("Stop Managing"), _("The device leaves the organization; pending actions are canceled"), "list-remove-symbolic");
            var retire_btn = new Button.with_label(_("Remove…"));
            retire_btn.valign = Align.CENTER;
            retire_btn.add_css_class("destructive-action");
            retire_btn.clicked.connect(() => confirm(_("Stop Managing %s?").printf(d.name),
                _("%s no longer receives actions or policies.").printf(d.name), _("Remove"), () => {
                    shown_device = null;
                    act("retire", d.id, "", _("%s is no longer managed").printf(d.name));
                }));
            retire.add_suffix(retire_btn);
            danger.add_row(retire);
            device_box.append(danger);

            if (commands.size > 0) {
                var history = new PreferencesGroup(_("Recent Actions"));
                foreach (var c in commands) {
                    var row = new ActionRow(kind_label(c.kind), state_label(c.state) + " · " + ago(c.created) + " · " + c.by,
                        c.state == "failed" ? "dialog-error-symbolic" : "emblem-ok-symbolic");
                    if (c.output != "") row.tooltip_text = c.output;
                    history.add_row(row);
                }
                device_box.append(history);
            }
            stack.visible_child_name = "device";
        }

        private void show_requests(Gee.List<Command> waiting) {
            clear(requests_box);
            if (waiting.size == 0) {
                var empty = new StatusPage();
                empty.icon_name = "emblem-ok-symbolic";
                empty.title = _("Nothing to Approve");
                empty.description = _("Erasing a device needs a second administrator. Those requests appear here.");
                requests_box.append(empty);
                stack.visible_child_name = "requests";
                return;
            }
            var group = new PreferencesGroup(_("Waiting for Approval"));
            foreach (var c in waiting) {
                string device = names.lookup(c.device) ?? c.device;
                var row = new ActionRow(_("%s on %s").printf(kind_label(c.kind), device), _("Asked by %s, %s").printf(c.by, ago(c.created)), "dialog-warning-symbolic");
                var cancel = new Button.with_label(_("Cancel"));
                cancel.valign = Align.CENTER;
                string id = c.id;
                cancel.clicked.connect(() => act("cancel", id, "", _("Request canceled")));
                row.add_suffix(cancel);
                if (c.by != fleet.me) {
                    var approve = new Button.with_label(_("Approve…"));
                    approve.valign = Align.CENTER;
                    approve.add_css_class("destructive-action");
                    approve.clicked.connect(() => confirm(_("Approve: %s on %s?").printf(kind_label(c.kind), device),
                        _("%s asked for this. Once approved, %s carries it out at its next check-in.").printf(c.by, device), _("Approve"),
                        () => act("approve", id, "", _("Approved"))));
                    row.add_suffix(approve);
                }
                group.add_row(row);
            }
            requests_box.append(group);
            stack.visible_child_name = "requests";
        }

        private void show_activity(Gee.List<Entry> entries, HashTable<string, string> names) {
            clear(activity_box);
            var group = new PreferencesGroup(null, _("Signed record of every change, newest first."));
            foreach (var e in entries) {
                string who = e.actor.has_prefix("device:") ? (names.lookup(e.actor.substring(7)) ?? _("A device")) : e.actor;
                var row = new ActionRow(who + ": " + describe_entry(e, names), e.time != null ? e.time.to_local().format("%x %X") : "", null);
                group.add_row(row);
            }
            activity_box.append(group);
            stack.visible_child_name = "activity";
        }

        private string describe_entry(Entry e, HashTable<string, string> names) {
            string device = names.lookup(e.target) ?? e.target;
            string kind = kind_label(e.detail.lookup("kind") ?? "").down();
            switch (e.action) {
                case "token.create": return _("created an enrollment code for %s").printf(e.target);
                case "policy.set": return _("changed the %s policy").printf(e.target);
                case "device.enroll": return _("joined the %s group").printf(e.detail.lookup("group") ?? "");
                case "device.retire": return _("removed %s from management").printf(device);
                case "command.create":
                    return e.detail.lookup("state") == "waiting_approval"
                        ? _("asked %s to %s, waiting for a second administrator").printf(device, kind)
                        : _("asked %s to %s").printf(device, kind);
                case "command.approve": return _("approved: %s on %s").printf(kind, device);
                case "command.cancel": return _("canceled: %s on %s").printf(kind, device);
                case "command.done": return _("%s done").printf(kind);
                case "command.failed": return _("%s failed").printf(kind);
                case "command.expired": return _("%s expired").printf(kind);
            }
            return e.action;
        }

        private ActionRow check_row(string title, bool good, string yes, string no) {
            return new ActionRow(title, good ? yes : no, good ? "emblem-ok-symbolic" : "dialog-warning-symbolic");
        }

        private void act(string op, string target, string kind, string done) {
            var f = fleet;
            if (f == null) return;
            perform.begin(f, op, target, kind, (obj, res) => {
                try {
                    perform.end(res);
                    toast(done);
                } catch (FleetError.STRONGER e) {
                    stack.visible_child_name = "stronger";
                    return;
                } catch (Error e) {
                    toast(e.message);
                }
                refresh();
            });
        }

        private static async void perform(Fleet f, string op, string target, string kind) throws Error {
            switch (op) {
                case "send": yield f.send(target, kind); break;
                case "approve": yield f.approve(target); break;
                case "cancel": yield f.cancel(target); break;
                case "retire": yield f.retire(target); break;
            }
        }

        private delegate void Confirmed();

        private void confirm(string title, string body, string action, owned Confirmed then) {
            var dlg = new ConfirmDialog(app, title, "dialog-warning-symbolic", body, action, ConfirmDialog.ActionStyle.DESTRUCTIVE);
            dlg.transient_for = this;
            dlg.response.connect((r) => {
                if (r == ConfirmDialog.Response.PRIMARY) then();
            });
            dlg.present();
        }

        private void step_up() {
            if (fleet == null) return;
            stack.visible_child_name = "loading";
            fleet.sign_in_again.begin((obj, res) => {
                try {
                    fleet.sign_in_again.end(res);
                } catch (Error e) {
                    toast(e.message);
                }
                refresh();
            });
        }

        private void toast(string text) {
            add_toast(new Toast(text));
        }

        private static string describe(Device d) {
            string[] parts = {};
            if (d.model != "") parts += d.model;
            if (d.os != "") parts += (d.os + " " + d.version).strip();
            if (d.channel != "") parts += d.channel;
            return parts.length > 0 ? string.joinv(" · ", parts) : d.hostname;
        }

        private static string problem_list(string[] issues) {
            string[] words = {};
            foreach (string i in issues) {
                switch (i) {
                    case "not_encrypted": words += _("Not encrypted"); break;
                    case "not_sealed": words += _("Keys not sealed to this hardware"); break;
                    case "firewall_off": words += _("Firewall off"); break;
                    case "not_seen": words += _("Not seen for a week"); break;
                    default: words += i; break;
                }
            }
            return string.joinv(", ", words);
        }

        public static string kind_label(string kind) {
            switch (kind) {
                case "lock": return _("Lock Screens");
                case "logout": return _("Sign Everyone Out");
                case "reboot": return _("Restart");
                case "update": return _("Install Updates");
                case "sync-policy": return _("Apply Policy");
                case "wipe": return _("Erase");
            }
            return kind;
        }

        private static string update_label(string state) {
            switch (state) {
                case "idle": return _("Up to date");
                case "available": return _("An update is available");
                case "downloading": return _("Downloading an update");
                case "ready": return _("Update ready, restarts to finish");
                case "error": return _("The last update check failed");
            }
            return state;
        }

        private static string state_label(string state) {
            switch (state) {
                case "waiting_approval": return _("Waiting for approval");
                case "queued": return _("Queued");
                case "sent": return _("Sent");
                case "done": return _("Done");
                case "failed": return _("Failed");
                case "expired": return _("Expired");
                case "canceled": return _("Canceled");
            }
            return state;
        }

        private static string ago(DateTime? when) {
            if (when == null) return _("Never");
            double s = double.max(0, (new DateTime.now_utc().difference(when)) / 1000000.0);
            if (s < 90) return _("Just now");
            if (s < 5400) return ngettext("%d minute ago", "%d minutes ago", (ulong) (s / 60)).printf((int) Math.round(s / 60));
            if (s < 129600) return ngettext("%d hour ago", "%d hours ago", (ulong) (s / 3600)).printf((int) Math.round(s / 3600));
            return ngettext("%d day ago", "%d days ago", (ulong) (s / 86400)).printf((int) Math.round(s / 86400));
        }
    }
}
