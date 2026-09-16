# tofu-resgen

Генератор модулей OpenTofu (`variables.tf`, скелет `main.tf` и пример
`terraform.tfvars.example`) по JSON-схеме провайдера.

Инструмент читает схему, полученную командой `tofu providers schema -json`, и выдаёт
файл переменных модуля. Сам провайдер при этом **не нужен**: достаточно сохранённой
схемы, поэтому генерация работает в offline и в CI, где провайдер недоступен.

Ключевой принцип: **генерируется только то, что однозначно выводится из схемы**.
`variables.tf` — буквальное отражение атрибутов ресурса: без ручных абстракций,
переименований и «умных» преобразований. `main.tf` — скелет: провайдер и блок
`resource`, где каждый атрибут подключён к переменной. `terraform.tfvars.example` —
один заполненный заглушками инстанс переменной. Проводка `data`-источников
из схемы не выводится (в ней нет связи «ресурс → data-источник»), поэтому пишется
человеком.

## Требования

- Go 1.26+ — для установки через `go install` и для сборки.
- OpenTofu/Terraform — только чтобы получить саму схему (`tofu providers schema -json`).
  Для работы генератора не требуется.

## Установка

```bash
go install github.com/ivaninkv/tofu-resgen/cmd/tofu-resgen@latest

# или явно
go install github.com/ivaninkv/tofu-resgen/cmd/tofu-resgen@v1.0.0
```

Модуль публичный, клонировать репозиторий не нужно. Бинарь кладётся в `GOBIN`
(по умолчанию `$(go env GOPATH)/bin`) — проверьте, что каталог в `PATH`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
command -v tofu-resgen
```

Если прокси в вашем контуре недоступен, поставьте `GOPROXY=direct` — модуль тянется
напрямую из GitHub (этим же способом можно взять ветку `master`):

```bash
GOPROXY=direct go install github.com/ivaninkv/tofu-resgen/cmd/tofu-resgen@master
```

### Почему тег `v0.1.0` берут в обход прокси

История репозитория сведена в один коммит, и тег `v0.1.0` переставлен на него. Но
`proxy.golang.org` успел закэшировать прежнюю историю, а версии в модульном прокси
неизменяемы:

| ref | что отдаёт прокси | что лежит на GitHub |
|---|---|---|
| `@v1.0.0` | текущий код (прокси взял его из GitHub) | тег `v1.0.0` |
| `@v0.1.0` | прежняя сборка (коммит `9303bb7`, без режима `-module`) | тег `v0.1.0` (текущий код) |
| `@latest` | `v0.3.0` (коммит `cc863a2`) — пока прокси не перечитает список тегов | тег `v1.0.0` |
| `@master` | `v0.3.0` (коммит `cc863a2`) | ветка `master` |

Практический вывод: **берите `v1.0.0`** — этот номер прокси увидел впервые, поэтому
под ним лежит актуальный код, а checksum-база записала его хеш. Номер `v0.1.0`
из прокси уже не исправить: под ним навсегда останется прежнее содержимое и прежний
хеш в `sum.golang.org`, поэтому установка именно этого тега требует обхода прокси и
проверки контрольных сумм (`GOPRIVATE` отключает и то, и другое; одного
`GOPROXY=direct` мало — установка упадёт на `checksum mismatch`):

```bash
GOPRIVATE=github.com/ivaninkv/tofu-resgen go install github.com/ivaninkv/tofu-resgen/cmd/tofu-resgen@v0.1.0

# то же самое, если GOPROXY задан явно
GOPROXY=direct GOSUMDB=off go install github.com/ivaninkv/tofu-resgen/cmd/tofu-resgen@v0.1.0
```

Проверено: скачанный так модуль `v0.1.0` побайтово совпадает с деревом репозитория.
Строки `@latest`/`@master` верны на момент записи кэша — если прокси перечитает теги,
`curl https://proxy.golang.org/github.com/ivaninkv/tofu-resgen/@latest` это покажет.

### Сборка из исходников

```bash
git clone https://github.com/ivaninkv/tofu-resgen
cd tofu-resgen
go build -o tofu-resgen ./cmd/tofu-resgen

# либо установить бинарь из локального дерева
go install ./cmd/tofu-resgen
```

## Как получить схему

```bash
mkdir -p .schema && cd .schema
cat > main.tf <<'EOF'
terraform {
  required_providers {
    yandex = {
      source = "yandex-cloud/yandex"
    }
  }
}
EOF
tofu init -backend=false
tofu providers schema -json > schema.json
```

Если реестр недоступен, можно указать локальный (filesystem mirror) каталог с уже
скачанным провайдером через `TF_CLI_CONFIG_FILE`:

```hcl
provider_installation {
  filesystem_mirror { path = "/path/to/mirror" }
  direct { exclude = ["registry.opentofu.org/*/*"] }
}
```

Раскладка mirror-каталога: `<host>/<namespace>/<type>/<version>/<platform>/terraform-provider-<type>_v<version>`.

## Использование

```bash
tofu-resgen -schema testdata/yandex.json -resource yandex_compute_instance -out compute/variables.tf
```

Имя переменной по умолчанию выводится из типа ресурса: отбрасывается префикс
провайдера и суффикс `_instance`, поэтому `yandex_compute_instance` → `compute`,
`yandex_vpc_network` → `vpc_network`. Переопределяется флагом `-var-name`.

### Режим `-module`

`-module <каталог>` вместо одиночного файла пишет комплект модуля: `variables.tf`,
`main.tf` с блоком `resource` на каждый атрибут схемы, который пользователь может
задать, и пример значений `terraform.tfvars.example`. С `-all` каждый ресурс
получает свой подкаталог `<каталог>/<ресурс>/`. `-out` и `-module` взаимоисключающие.

Файл называется `terraform.tfvars.example`, а не `terraform.tfvars`: последний
OpenTofu загружает автоматически, и план падал бы на незаполненных заглушках.
Пример копируют в рабочий `terraform.tfvars`.

`data`-источники в `main.tf` не генерируются: схема провайдера их не описывает (см.
«Ограничения»). Полученный `main.tf` — точка старта, его правят руками.

### Флаги

| Флаг | Значение |
|---|---|
| `-schema` | путь к JSON-схеме (`tofu providers schema -json`) — обязателен |
| `-resource` | тип ресурса, например `yandex_compute_instance` (не нужен с `-all`) |
| `-var-name` | имя переменной (по умолчанию выводится из типа ресурса) |
| `-provider` | адрес провайдера: полный (`registry.opentofu.org/yandex-cloud/yandex`), относительный (`yandex-cloud/yandex`) или имя типа (`yandex`); нужен, только если в схеме несколько провайдеров |
| `-defaults` | YAML-файл значений по умолчанию для optional-атрибутов |
| `-out` | файл вывода или `-` для stdout; с `-all` — каталог |
| `-module` | каталог модуля: `variables.tf`, `main.tf` и `terraform.tfvars.example` (с `-all` — подкаталог на ресурс) |
| `-tfvars-optional` | показать в примере tfvars и optional-атрибуты, закомментированными, со значением, которое они получают по умолчанию |
| `-description` | переопределить описание переменной |
| `-doc` | выводить описания из схемы комментариями (по умолчанию `true`) |
| `-all` | сгенерировать файл на каждый ресурс провайдера в каталог `-out` |
| `-check` | проверить соответствие схеме, ничего не записывая; с `-module` проверяет файлы на диске |
| `-refresh-schema` | выполнить `tofu providers schema -json` и закэшировать результат в `-schema` |
| `-tofu-bin` | бинарь `tofu` для `-refresh-schema` (по умолчанию `tofu`) |

### Примеры

```bash
# один ресурс
tofu-resgen -schema schema.json -resource yandex_vpc_network -out vpc/variables.tf

# комплект модуля: variables.tf, main.tf и пример terraform.tfvars.example
tofu-resgen -schema schema.json -resource yandex_vpc_subnet -module subnet

# комплект модуля с дефолтами
tofu-resgen -schema schema.json -resource yandex_vpc_network \
    -defaults defaults/yandex_vpc_network.yaml -module vpc

# пример tfvars с optional-атрибутами (закомментированы, со значением по умолчанию)
tofu-resgen -schema schema.json -resource yandex_vpc_subnet \
    -module subnet -tfvars-optional

# все ресурсы провайдера
tofu-resgen -schema schema.json -all -out ./modules
tofu-resgen -schema schema.json -all -module ./modules

# проверить соответствие схеме, ничего не писать (код выхода 1 при расхождении)
tofu-resgen -schema schema.json -resource yandex_compute_instance -check
tofu-resgen -schema schema.json -all -check

# проверить уже сгенерированный модуль на диске
tofu-resgen -schema schema.json -resource yandex_vpc_subnet -module subnet -check
tofu-resgen -schema schema.json -all -module ./modules -check

# обновить схему прямо из провайдера
tofu-resgen -provider yandex-cloud/yandex -schema schema.json -refresh-schema
```

## Правила генерации

Каждый атрибут ресурса, который пользователь может задать, попадает в объект
элемента `map`. Типы и режимы вложенности берутся из схемы без изменений.

| В схеме | В `variables.tf` |
|---|---|
| `required = true` | `name = <T>` |
| `optional = true` (в т.ч. `+ computed`) | `name = optional(<T>)` |
| `optional = true` + значение в `-defaults` | `name = optional(<T>, <значение>)` |
| `computed = true` без `optional`/`required` | исключается (пользователь не задаёт такие поля) |
| `id` | исключается: идентификатор ведёт протокол плагина, OpenTofu не принимает его как аргумент ресурса, даже когда провайдер помечает поле `optional` |
| legacy `block_types` | нормализуется в атрибут с соответствующим `nesting_mode` |

Типы (`<T>`):

| Схема | HCL |
|---|---|
| `"string"`, `"number"`, `"bool"` | `string`, `number`, `bool` |
| `["list", X]`, `["set", X]`, `["map", X]` | `list(X)`, `set(X)`, `map(X)` |
| `["object", {…}]` | `object({ … })` |
| `nesting_mode = single/list/set/map` | `object({…})` / `list(object({…}))` / `set(object({…}))` / `map(object({…}))` |

Прочее:

- поля сортируются по алфавиту — вывод детерминирован (побайтово одинаков при повторном запуске);
- описания из схемы выводятся комментариями над полями, отключаются `-doc=false`;
- переменная имеет `default = {}`, поэтому модуль с нулём экземпляров валиден;
- результат прогоняется через `hclwrite.Format`, то есть совпадает с `tofu fmt`.

### Пример вывода

Сокращённый фрагмент реального вывода (`-doc=false`):

```hcl
# Generated by tofu-resgen from the provider schema.

variable "compute" {
  description = "Configuration for yandex_compute_instance (map: instance name → object)."
  type = map(object({
    allow_recreate = optional(bool)
    boot_disk = list(object({
      auto_delete = optional(bool)
      disk_id     = optional(string)
    }))
    description = optional(string)
    filesystem = optional(set(object({
      device_name   = optional(string)
      filesystem_id = string
      mode          = optional(string)
    })))
    labels  = optional(map(string))
    timeouts = optional(object({
      create = optional(string)
    }))
    zone = optional(string)
  }))
  default = {}
}
```

## Правила генерации `main.tf`

Генерируется только `-module`-режимом. Из схемы выводятся две вещи:

| Из чего | Что в `main.tf` |
|---|---|
| адрес провайдера | блок `terraform.required_providers` |
| атрибуты ресурса | блок `resource "<тип>" "<имя переменной>"` |
| каждый `required`/`optional` (+`optional+computed`) атрибут | `<атрибут> = each.value.<атрибут>` |
| legacy `block_types` | `dynamic "<блок>" { for_each = … content { … } }` (рекурсивно) |
| `computed` без `optional`/`required`, `id` | не выводится |
| переменная модуля | `for_each = var.<имя переменной>` |

Legacy-блоки рендерятся как `dynamic`, потому что ресурс принимает их как блоки
(`timeouts { … }`), а не как аргументы — OpenTofu отвергает `timeouts = { … }`.
Блок не создаётся, когда переменная не задана:

```hcl
dynamic "timeouts" {
  for_each = each.value.timeouts == null ? [] : [each.value.timeouts]
  content {
    create = timeouts.value.create
    # ...
  }
}
```

Проводка — тождество: тип переменной в `variables.tf` объявлен ровно по схеме
ресурса, поэтому `each.value.<атрибут>` типобезопасен для каждого атрибута.

Пример (`-module subnet`, фрагмент):

```hcl
resource "yandex_vpc_subnet" "vpc_subnet" {
  for_each = var.vpc_subnet

  dynamic "dhcp_options" {
    for_each = each.value.dhcp_options == null ? [] : each.value.dhcp_options
    content {
      domain_name = dhcp_options.value.domain_name
      # ...
    }
  }
  name           = each.value.name
  network_id     = each.value.network_id
  v4_cidr_blocks = each.value.v4_cidr_blocks
  zone           = each.value.zone
}
```

`data`-источники здесь не появляются: см. «Ограничения». Если атрибут на практике
приходит из `data`-источника (в примере — `network_id`), его подключение дописывается
руками; `-check` такое ручное подключение расхождением не считает.

## Правила генерации `terraform.tfvars.example`

Генерируется только `-module`-режимом. Файл описывает один инстанс переменной
(`example`) и ровно те поля, которые модуль требует от пользователя.

| Из чего | Что в `terraform.tfvars.example` |
|---|---|
| `required` атрибут | активная строка с заглушкой: `"<имя>"`, `number` → `0`, `bool` → `false` |
| `optional` атрибут | не выводится; с `-tfvars-optional` — закомментированная строка |
| `optional` со значением по умолчанию | в комментарии стоит это значение (`optional(T, v)` или `-defaults`) |
| `required` коллекция | один элемент: `["<x.item>"]`, `{ "<x.key>" = … }`, `[{ … }]` |
| `computed` без `optional`/`required`, `id` | не выводится |

Заглушка — это путь атрибута внутри инстанса, с сегментами коллекций: `item` для
`list`/`set`, `key` для `map`. Поэтому всё, что осталось заполнить, находится одним
`grep '<'`.

Пример (`-module subnet -tfvars-optional`):

```hcl
vpc_subnet = {
  example = {
    # description = "<description>"
    # dhcp_options = [{}]
    # folder_id = "<folder_id>"
    # labels = {
    #   "<labels.key>" = "<labels.key>"
    # }
    # name = "<name>"
    network_id = "<network_id>"
    # route_table_id = "<route_table_id>"
    # timeouts = {}
    v4_cidr_blocks = ["<v4_cidr_blocks.item>"]
    # zone = "<zone>"
  }
}
```

Атрибут со значением по умолчанию показан этим значением, без значения — заглушкой.
Обязательная коллекция, у которой все вложенные поля `optional`, выводится пустым
элементом (`# dhcp_options = [{}]`): схема не говорит, какое поле обязательно.

`-check` сверяет пример со схемой, если файл есть на диске: неизвестный атрибут,
пропущенный `required`, противоречивая форма значения, переменная, которой модуль не
объявляет. Имена и число инстансов, конкретные значения и нелитеральные выражения
расхождением не считаются — файл для того и создан, чтобы его правили руками.

## Файл дефолтов

YAML-файл со значениями для optional-атрибутов; вложенность задаётся вложенными
отображениями:

```yaml
description: "managed by tofu-resgen"
labels:
  managed-by: tofu-resgen
timeouts:
  create: "10m"
```

Значения валидируются по схеме до генерации:

- атрибут должен существовать;
- на верхнем уровне — быть `optional` (required-атрибуту дефолт не нужен и запрещён);
- литерал должен совпадать по типу со схемой;
- внутри объектного литерала все required-поля обязаны присутствовать.

Нарушение — ошибка с путём и кодом выхода 3.

## Коды выхода

| Код | Значение |
|---|---|
| 0 | успех |
| 1 | `-check` нашёл расхождения со схемой |
| 2 | ошибка входа или схемы (файл, провайдер, ресурс, парсинг) |
| 3 | ошибка файла дефолтов |

## Структура репозитория

```
cmd/tofu-resgen/    CLI
internal/schema/    модель схемы (format_version 1.0) и загрузка
internal/tfgen/     рендер типов, объект переменной, дефолты, скелет main.tf, пример tfvars
internal/verify/    независимая сверка «вывод ⟺ схема» для variables.tf, main.tf и tfvars
testdata/           фикстура схемы публичного провайдера для тестов
defaults/           примеры файлов дефолтов
local/              локальные входные данные, не версионируется (см. .gitignore)
```

### Зачем `testdata/`

`testdata/yandex.json` — сохранённая схема **публичного** провайдера
`registry.opentofu.org/yandex-cloud/yandex` v0.228.0 (253 ресурса, 153
data-источника). Она нужна, чтобы тесты были воспроизводимы и не зависели от сети:

- `internal/verify` прогоняет генерацию по всем ресурсам и сверяет её со схемой;
- `internal/schema` и `internal/tfgen` проверяют на ней разбор схемы, режимы
  вложенности, legacy `block_types` и валидацию дефолтов.

Каталог можно удалить, только если убрать и зависящие от него тесты. Обновить фикстуру
под новую версию провайдера:

```bash
# см. раздел «Как получить схему»
cp schema.json testdata/yandex.json
go test ./...
```

Каталог `local/` добавлен в `.gitignore` и предназначен для приватных входных
данных (закрытые схемы, примеры модулей) — в репозиторий они не попадают.

## Разработка

```bash
go build ./...
go vet ./...
go test ./...
```

Проверки на реальном OpenTofu (для сгенерированного модуля):

```bash
tofu fmt -check -diff
tofu validate
```

## Ограничения

- **`data`-источники не генерируются.** Схема провайдера (`format_version 1.0`)
  описывает атрибуты, типы и режимы, но не связи: в ней нет ни того, какие
  `data`-источники нужны ресурсу, ни того, какой выход источника подставляется в
  какой атрибут. Проверено на фикстуре `testdata/yandex.json`: ни одно описание
  атрибута не ссылается на имя `data`-источника. Совпадение имён
  (`yandex_vpc_subnet.network_id` и гипотетический `data.yandex_vpc_network`) —
  догадка, а не выводимый из схемы факт. Поэтому проводка `data`-источников,
  null-гварды и `outputs.tf` пишутся вручную поверх сгенерированного скелета.
- **`main.tf` не претендует на рабочую конфигурацию.** Блок `resource` подключает
  всё к переменным; атрибуты, которые в реальном модуле берутся из `data`
  (в примере выше — `network_id`), придётся переключить руками.
  Схема не отличает их от обычных `required`-атрибутов.
- **`terraform.tfvars.example` — не готовое значение.** Заглушки вроде
  `"<network_id>"` проходят проверку типов, но провайдер их отвергнет: файл
  копируют и заполняют. Обязательная коллекция, у которой все вложенные поля
  `optional`, выводится пустым элементом (`boot_disk = [{}]`) — из схемы нельзя
  узнать, какое поле обязательно.
- `required`-атрибуты остаются обязательными. Если ресурс вычисляет часть полей
  сам (например, наполняет вложенный блок из `data`-источника), в переменных эти
  поля всё равно появятся как обязательные — это осознанный выбор в пользу
  точного соответствия схеме.
