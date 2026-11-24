project('sinty-it-app', ['vala', 'c'],
  version: '0.1.0',
  meson_version: '>= 0.59.0'
)

i18n = import('i18n')
add_project_arguments('-DGETTEXT_PACKAGE="sinty-it"', language: 'c')
subdir('po')

libsingularity_dep = dependency('singularity-1.0',
  fallback: ['libsingularity', 'libsingularity_dep']
)

if not meson.is_subproject() and libsingularity_dep.type_name() == 'internal'
  libsingularity_vapi_dir = join_paths(meson.project_build_root(), 'subprojects', 'libsingularity')
  add_project_arguments(['--vapidir=' + libsingularity_vapi_dir], language: 'vala')
endif

executable('sinty-it-app',
  files('src/fleet.vala', 'src/window.vala', 'src/main.vala'),
  dependencies: [
    dependency('gtk4'),
    dependency('gee-0.8'),
    dependency('gio-2.0'),
    dependency('json-glib-1.0'),
    dependency('libsoup-3.0'),
    meson.get_compiler('c').find_library('m'),
    libsingularity_dep,
  ],
  install: true
)

data_conf = configuration_data()
data_conf.set('bindir', get_option('prefix') / get_option('bindir'))

configure_file(
  input: 'data/dev.sinty.IT.service.in',
  output: 'dev.sinty.IT.service',
  configuration: data_conf,
  install_dir: get_option('datadir') / 'dbus-1' / 'services'
)

install_data('data/dev.sinty.IT.desktop',
  install_dir: get_option('datadir') / 'applications'
)
