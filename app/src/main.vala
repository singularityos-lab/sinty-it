namespace Sinty.It {

    public class ItApp : Singularity.Application {
        public ItApp() {
            Object(application_id: "dev.sinty.IT", flags: ApplicationFlags.DEFAULT_FLAGS);
        }

        protected override void activate() {
            var window = active_window as ItWindow;
            if (window == null) window = new ItWindow(this);
            window.present();
        }
    }
}

int main(string[] args) {
    Intl.setlocale(LocaleCategory.ALL, "");
    string locale_dir = "/usr/share/locale";
    try {
        string exe = FileUtils.read_link("/proc/self/exe");
        locale_dir = Path.build_filename(Path.get_dirname(Path.get_dirname(exe)), "share", "locale");
    } catch (Error e) {
    }
    Intl.bindtextdomain("sinty-it", locale_dir);
    Intl.bind_textdomain_codeset("sinty-it", "UTF-8");
    Intl.textdomain("sinty-it");
    Environment.set_application_name(_("Sinty IT"));
    return new Sinty.It.ItApp().run(args);
}
