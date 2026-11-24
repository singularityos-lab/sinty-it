using Singularity.Accounts;

namespace Sinty.It {

    public errordomain FleetError {
        STRONGER,
        REFUSED,
        FAILED
    }

    public class Device : Object {
        public string id = "";
        public string name = "";
        public string group = "";
        public DateTime? seen;
        public DateTime? enrolled;
        public string hostname = "";
        public string os = "";
        public string version = "";
        public string arch = "";
        public string model = "";
        public string channel = "";
        public string last_user = "";
        public string boot_health = "";
        public int64 disk_free = 0;
        public bool encrypted = false;
        public bool sealed = false;
        public bool firewall = false;
        public string[] issues = {};

        public Device.from_json(Json.Object o) {
            id = text(o, "id");
            name = text(o, "name");
            group = text(o, "group");
            seen = time(o, "seen");
            enrolled = time(o, "enrolled");
            var f = o.has_member("facts") ? o.get_object_member("facts") : new Json.Object();
            hostname = text(f, "hostname");
            os = text(f, "os");
            version = text(f, "version");
            arch = text(f, "arch");
            model = text(f, "model");
            channel = text(f, "channel");
            last_user = text(f, "last_user");
            boot_health = text(f, "boot_health");
            disk_free = f.has_member("disk_free") ? f.get_int_member("disk_free") : 0;
            encrypted = f.has_member("encrypted") && f.get_boolean_member("encrypted");
            sealed = f.has_member("sealed") && f.get_boolean_member("sealed");
            firewall = f.has_member("firewall") && f.get_boolean_member("firewall");
            if (o.has_member("issues")) {
                string[] found = {};
                foreach (var n in o.get_array_member("issues").get_elements()) found += n.get_string();
                issues = found;
            }
        }
    }

    public class Command : Object {
        public string id = "";
        public string device = "";
        public string kind = "";
        public string by = "";
        public string state = "";
        public string output = "";
        public DateTime? created;

        public Command.from_json(Json.Object o) {
            id = text(o, "id");
            device = text(o, "device");
            kind = text(o, "kind");
            by = text(o, "by");
            state = text(o, "state");
            output = text(o, "output");
            created = time(o, "created");
        }
    }

    public class Entry : Object {
        public DateTime? time;
        public string actor = "";
        public string action = "";
        public string target = "";
        public HashTable<string, string> detail = new HashTable<string, string>(str_hash, str_equal);

        public Entry.from_json(Json.Object o) {
            time = Sinty.It.time(o, "time");
            actor = text(o, "actor");
            action = text(o, "action");
            target = text(o, "target");
            if (o.has_member("detail") && o.get_member("detail").get_node_type() == Json.NodeType.OBJECT) {
                var d = o.get_object_member("detail");
                foreach (string k in d.get_members()) detail.insert(k, d.get_string_member(k));
            }
        }
    }

    internal string text(Json.Object o, string key) {
        if (!o.has_member(key) || o.get_member(key).get_value_type() != typeof(string)) return "";
        return o.get_string_member(key);
    }

    internal DateTime? time(Json.Object o, string key) {
        string v = text(o, key);
        if (v == "" || v.has_prefix("0001-")) return null;
        return new DateTime.from_iso8601(v, new TimeZone.utc());
    }

    public class Fleet : Object {
        public Account account { get; construct; }
        public string base_url { get; construct; }
        public string me { get; private set; default = ""; }
        public string wanted_acr { get; private set; default = ""; }
        private HttpClient http;

        public Fleet(Account account, string base_url) {
            Object(account: account, base_url: base_url.has_suffix("/") ? base_url.substring(0, base_url.length - 1) : base_url);
            http = new HttpClient(account, Capability.DEVICES);
        }

        public static string? address_of(Account account) {
            if (!account.has_capability(Capability.DEVICES)) return null;
            string? url = account.get_endpoint("sinty-it");
            return url != null && url != "" ? url : null;
        }

        private async Json.Object call(string method, string path, Json.Builder? body = null) throws Error {
            Bytes? payload = null;
            if (body != null) {
                var gen = new Json.Generator();
                gen.root = body.get_root();
                payload = new Bytes(gen.to_data(null).data);
            }
            var r = yield http.send(method, base_url + path, payload != null ? "application/json" : null, payload);
            string? acr = r.step_up_acr();
            if (acr != null) {
                wanted_acr = acr != "" ? acr : "sinty:3";
                throw new FleetError.STRONGER(_("Confirm on one of your devices to manage this organization."));
            }
            if (r.status == 403) throw new FleetError.REFUSED(_("%s does not manage devices in this organization.").printf(account.identity));
            if (r.status == 204) return new Json.Object();
            if (!r.ok) {
                string code = "";
                try {
                    code = text(r.json_object(), "error");
                } catch (Error e) {
                }
                throw new FleetError.FAILED(message_for(code, r.status));
            }
            return r.json_object();
        }

        private static string message_for(string code, uint status) {
            switch (code) {
                case "second_admin": return _("Another administrator has to approve this.");
                case "not_waiting": return _("This request is no longer waiting.");
                case "not_found": return _("The device is no longer managed.");
                case "unknown_command": return _("This device does not know that action.");
            }
            return _("The server answered with an error (%u).").printf(status);
        }

        public async void sign_in_again() throws Error {
            yield account.step_up(wanted_acr != "" ? wanted_acr : "sinty:3");
        }

        public async void load_me() throws Error {
            me = text(yield call("GET", "/api/v1/me"), "user");
        }

        public async Gee.List<Device> devices() throws Error {
            var list = new Gee.ArrayList<Device>();
            var o = yield call("GET", "/api/v1/devices");
            foreach (var n in o.get_array_member("devices").get_elements()) list.add(new Device.from_json(n.get_object()));
            return list;
        }

        public async Device device(string id, Gee.List<Command> commands) throws Error {
            var o = yield call("GET", "/api/v1/devices/" + Uri.escape_string(id));
            foreach (var n in o.get_array_member("commands").get_elements()) commands.add(new Command.from_json(n.get_object()));
            return new Device.from_json(o.get_object_member("device"));
        }

        public async Gee.List<Command> waiting() throws Error {
            var list = new Gee.ArrayList<Command>();
            var o = yield call("GET", "/api/v1/commands?state=waiting_approval");
            foreach (var n in o.get_array_member("commands").get_elements()) list.add(new Command.from_json(n.get_object()));
            return list;
        }

        public async void send(string device, string kind) throws Error {
            var b = new Json.Builder();
            b.begin_object();
            b.set_member_name("kind");
            b.add_string_value(kind);
            b.end_object();
            yield call("POST", "/api/v1/devices/" + Uri.escape_string(device) + "/commands", b);
        }

        public async void approve(string command) throws Error {
            yield call("POST", "/api/v1/commands/" + Uri.escape_string(command) + "/approve");
        }

        public async void cancel(string command) throws Error {
            yield call("POST", "/api/v1/commands/" + Uri.escape_string(command) + "/cancel");
        }

        public async void retire(string device) throws Error {
            yield call("DELETE", "/api/v1/devices/" + Uri.escape_string(device));
        }

        public async Gee.List<Entry> activity(HashTable<string, string> names) throws Error {
            var list = new Gee.ArrayList<Entry>();
            var o = yield call("GET", "/api/v1/audit");
            if (o.has_member("devices")) {
                var d = o.get_object_member("devices");
                foreach (string k in d.get_members()) names.insert(k, d.get_string_member(k));
            }
            foreach (var n in o.get_array_member("entries").get_elements()) list.add(new Entry.from_json(n.get_object()));
            return list;
        }
    }
}
