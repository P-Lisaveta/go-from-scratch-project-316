# Парсер сайтов (Go)

[![hexlet-check](https://github.com/P-Lisaveta/go-from-scratch-project-316/actions/workflows/hexlet-check.yml/badge.svg)](https://github.com/P-Lisaveta/go-from-scratch-project-316/actions)

Разработайте утилиту для обхода и анализа сайтов

Учебный проект Хекслета: https://ru.hexlet.io/programs/go-from-scratch


## Стек

- Go

## Установка

```bash
git clone https://github.com/P-Lisaveta/go-from-scratch-project-316.git
cd go-from-scratch-project-316
make build
```

Подготовьте зависимости и запустите локальную проверку:

```bash
make setup
make test
make lint
```

## Использование

Запустите анализ сайта через `make`:

```bash
make run URL=https://example.com
```

Или запустите CLI напрямую:

```bash
go run ./cmd/hexlet-go-crawler --depth 2 --workers 8 https://example.com
go run ./cmd/hexlet-go-crawler --help
```

Параметр `--depth` задаёт число уровней обхода с учётом стартовой страницы: у неё `depth: 0`, поэтому `--depth 1` анализирует только её, а `--depth 2` добавляет внутренние ссылки первого уровня. Внешние URL проверяются на доступность, но не попадают в список страниц отчёта.

Скорость запросов регулируется глобально для всего запуска: `--delay=200ms` задаёт минимальный интервал между запросами, а `--rps=5` — не более пяти запросов в секунду. Параметры соответствуют `Options.Delay` и `Options.RPS`; при одновременном указании `--rps` имеет приоритет.

Параметр `--retries=N` задаёт число повторов после первой попытки (`Options.Retries`), то есть при `--retries=2` запрос будет выполнен максимум три раза. Повторяются сетевые сбои, `429` и ответы `5xx`; другие `4xx` сразу считаются итоговым результатом. Между повторами краулер делает короткую паузу и прекращает попытки при отмене контекста.

## Формат JSON-отчёта

```json
{
  "root_url": "https://example.com",
  "depth": 1,
  "generated_at": "2024-06-01T12:34:56Z",
  "pages": [
    {
      "url": "https://example.com",
      "depth": 0,
      "http_status": 200,
      "status": "ok",
      "seo": {
        "has_title": true,
        "title": "Example title",
        "has_description": true,
        "description": "Example description",
        "has_h1": true
      },
      "broken_links": [
        {
          "url": "https://example.com/missing",
          "status_code": 404
        }
      ],
      "assets": [
        {
          "url": "https://example.com/static/logo.png",
          "type": "image",
          "status_code": 200,
          "size_bytes": 12345
        }
      ],
      "discovered_at": "2024-06-01T12:34:56Z"
    }
  ]
}
```

`root_url` и `depth` описывают запуск, `generated_at` — время формирования отчёта. В `pages` находятся URL, расстояние от стартовой страницы, статус HTTP-запроса и время обнаружения. Поле `error` добавляется при ошибке загрузки страницы или ассета. Блок `seo` содержит title, description и наличие h1. `broken_links` перечисляет недоступные ссылки, а `assets` — изображения, скрипты и стили с итоговым статусом и размером в байтах. Временные поля имеют формат ISO 8601. `IndentJSON` меняет только пробелы и переносы строк.

Команда `make test` запускает все автоматические тесты. Результат работы — JSON-отчёт с URL корневой страницы, временем формирования и статусом первого HTTP-запроса.

---

<details>
<summary>Автоматические тесты Хекслета</summary>

Тесты запускаются на каждый коммит. За запуск отвечает файл `.github/workflows/hexlet-check.yml` — не удаляйте и не переименовывайте ни его, ни репозиторий.

</details>

## О Хекслете

[Хекслет](https://ru.hexlet.io/) — школа программирования: авторские программы обучения с практикой, поддержкой наставников и реальными проектами, которые остаются в резюме. Этот репозиторий — один из таких проектов.
