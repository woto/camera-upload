# camera-upload

Микросервис для приёма загрузок больших видеофайлов (несколько ГБ) с
**возобновляемой загрузкой** (протокол [tus](https://tus.io)) и отслеживанием
**актуального процента загрузки**.

Сервис представляет собой один Go-бинарь, в который встроены:

- **tusd** (как библиотека) — реализует протокол tus на `/files/`: chunked-загрузка,
  возобновление после обрыва соединения, контроль размера.
- **управляющий API** в корне — статус, прогресс, список, скачивание, превью.
- **Uppy-клиент** на `/client` — простой веб-интерфейс с прогресс-баром и
  возобновлением после перезагрузки вкладки (Golden Retriever).

После завершения загрузки сервис проверяет seek OpenCV на 60 кадрах. Если
номера кадров ненадёжны, он сохраняет исходник и создаёт отдельную CFR-копию;
иначе исходник становится рабочим видео. Только после этого сервис извлекает
метаданные (`ffprobe`) и генерирует превью (`ffmpeg`).

## Эндпоинты

| Метод | Путь | Описание |
|-------|------|----------|
| `POST/PATCH/HEAD/DELETE` | `/files/...` | протокол tus (загрузка, возобновление) |
| `GET` | `/health` | проверка живости |
| `GET` | `/uploads` | список загрузок; фильтры `?q=` (по названию/имени файла), `?tag=` (повторяемый, AND), пагинация `?page=&page_size=` |
| `GET` | `/uploads/{id}` | статус, процент, длительность и видео-метаданные |
| `PATCH` | `/uploads/{id}` | задать `title` и `tags` (JSON-тело) |
| `DELETE` | `/uploads/{id}` | удалить загрузку и сайдкары |
| `GET` | `/uploads/{id}/download` | скачать завершённый файл |
| `GET` | `/uploads/{id}/original` | скачать исходно загруженный файл |
| `POST` | `/uploads/{id}/retry-processing` | повторить неудачную проверку/конвертацию |
| `GET` | `/uploads/{id}/frame?t=<sec>` | кадр (JPEG) на заданной секунде |
| `GET` | `/uploads/{id}/proxy?fps=&width=&gray=` | компактный прокси-клип (H.264) для анализаторов; генерится по запросу и кэшируется |
| `GET` | `/uploads/{id}/exports` | сохранённые версии прокси вместе с последним успешным motion analysis |
| `GET` | `/uploads/{id}/exports/{exportId}` | одна версия прокси и её motion analysis |
| `GET` | `/uploads/{id}/exports/{exportId}/proxy` | канонический прокси версии; query-параметры игнорируются |
| `PUT` | `/uploads/{id}/exports/{exportId}/analysis` | внутренняя запись результата camera-motion (Bearer token) |
| `GET` | `/uploads/{id}/thumbnail` | текущее JPEG-превью |
| `POST` | `/uploads/{id}/thumbnail` | пересоздать превью из кадра на секунде `t` (JSON `{"t": <sec>}`) |
| `GET` | `/tags` | объединённый список всех тегов (для автодополнения) |
| `GET` | `/client` | веб-интерфейс Uppy |

### Интеграция с camera-homography

`GET /uploads/{id}/frame?t=<sec>` отдаёт полноразмерный кадр. Этот URL можно
передать в camera-homography, которая теперь умеет создавать сессию по ссылке:

```bash
curl -X POST http://homography/sessions \
  -F "image_url=http://camera_upload:8000/uploads/<id>/frame?t=8" \
  -F "court_type=beach_volleyball"
```

Так между сервисами передаётся только кадр, а не всё видео. В UI (`/client`)
для этого есть кнопка **Frame** с выбором момента, копированием URL,
установкой превью и временной подборкой до восьми кадров. Подборку можно
передать в Camera SAM3: новая вкладка получает ссылки через `postMessage`, а
SAM3 скачивает кадры только при запуске анализа.

### Прокси для анализа движения (camera-motion)

Чтобы не гонять по сети многогигабайтный оригинал, `GET /uploads/{id}/proxy`
отдаёт **компактный клип**: тяжёлое декодирование идёт локально (файл на диске
здесь), а наружу уходит несколько МБ. Параметры: `fps` (кадров/с, по умолчанию
4), `width` (ширина, 480), `gray` (1 — grayscale, по умолчанию). Прокси
генерируется при первом запросе и кэшируется сайдкаром `{id}.proxy_*.mp4`.

```bash
# camera-motion анализирует лёгкий прокси, а не оригинал:
curl -X POST http://motion/jobs -d '{"video_url":
  "http://camera_upload:8000/uploads/<id>/proxy?fps=4&width=480&gray=1"}'
```

### Владение результатами camera-motion

`camera-upload` — единственный долговременный владелец диапазонов, в которых
камера была неподвижна. Последний успешно принятый результат хранится в поле
`analysis` соответствующей записи `{id}.exports.json` и доступен через список
и detail endpoint экспортов. Наличие этого поля определяет бейдж `analyzed` в
UI; отдельный presence-запрос к camera-motion не используется.

```json
{
  "id": "export-id",
  "fps": 4,
  "width": 480,
  "gray": true,
  "analysis": {
    "schema_version": 1,
    "analysis_id": "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527",
    "started_at": 1783844400,
    "created_at": 1783844515,
    "source": {"fps": 4, "width": 480, "gray": true},
    "duration": 20,
    "parameters": {
      "method": "affine", "mask": true, "roi": "full", "enter": 2,
      "settle": 0.5, "settle_samples": 2, "min_segment": 1,
      "features": 500, "min_inliers": 20
    },
    "segments": [
      {"start": 0, "end": 8, "kind": "stable"},
      {"start": 8, "end": 20, "kind": "transition"}
    ]
  }
}
```

Camera-motion получает доверенный источник через
`/uploads/{id}/exports/{exportId}/proxy`: параметры `fps`, `width` и `gray`
берутся из сохранённой версии, а query-переопределения игнорируются. Запись
анализа требует точный заголовок `Authorization: Bearer <CAMERA_INTERNAL_TOKEN>`;
тело ограничено 2 МиБ и строго валидируется. Изменение канонических настроек
версии атомарно удаляет прежний `analysis`, повторное сохранение тех же настроек
его сохраняет.

Операции с `{id}.exports.json` сериализуются внутри процесса и записываются
атомарной заменой файла. На один каталог данных допускается только один пишущий
процесс `camera-upload`; для нескольких процессов потребовалась бы отдельная
межпроцессная блокировка.

Пример ответа `GET /uploads/{id}`:

```json
{
  "id": "7cd61574a32d565aa27e55d75e4a0e9f",
  "filename": "big.mp4",
  "filetype": "video/mp4",
  "title": "Beach Final",
  "tags": ["beach", "final", "2026"],
  "size": 131072,
  "offset": 65536,
  "percent": 50,
  "completed": false,
  "duration": 20,
  "created_at": "2026-06-24T19:30:09Z",
  "has_thumbnail": false,
  "video_meta": { "...ffprobe JSON..." : "" }
}
```

## Конфигурация (переменные окружения)

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `PORT` | `8000` | порт HTTP-сервера |
| `DATA_DIR` | `./data` | каталог хранения загрузок и сайдкаров |
| `BASE_PATH` | `/files/` | URL-префикс протокола tus |
| `MAX_UPLOAD_SIZE` | `10737418240` (10 ГиБ) | максимальный размер одной загрузки в байтах |
| `THUMBNAILS` | `true` | генерировать превью |
| `INCOMPLETE_TTL` | `24h` | TTL незавершённых загрузок до автоочистки |
| `CAMERA_MOTION_EXTERNAL_URL` | — | browser-facing URL Camera Motion |
| `CAMERA_FISHEYE_EXTERNAL_URL` | — | browser-facing URL Camera Fisheye |
| `CAMERA_SAM3_EXTERNAL_URL` | — | browser-facing URL Camera SAM3 |
| `CAMERA_INTERNAL_TOKEN` | — | обязательный общий секрет для внутренних запросов camera-motion |

## Запуск локально

Требуются Go 1.25+ и `ffmpeg`/`ffprobe` в `PATH`.

Канонический локальный порт — **8200** (чтобы не конфликтовать с соседними
сервисами camera-motion `8100` и camera-homography `8300`; внутри контейнера
по умолчанию используется `8000`):

```bash
PORT=8200 go run ./cmd/server
# открыть http://localhost:8200/client

# порт можно задать флагом (переопределяет PORT) или переменной окружения:
go run ./cmd/server -port 8200
PORT=8200 go run ./cmd/server
```

## Запуск в Docker

```bash
docker compose up -d --build
```

Образ содержит `ffmpeg`. Данные хранятся в именованном volume `uploads`.
Маршрутизация — через Traefik (см. метки в `docker-compose.yml`).

## Тесты

```bash
go test ./...
```

Тесты постобработки (`internal/process`) требуют `ffmpeg`/`ffprobe` и
автоматически пропускаются при их отсутствии.

## Архитектурные заметки

- **Прогресс без БД.** Источник истины — каталог `DATA_DIR`. tusd хранит на
  загрузку файл данных `{id}` и `{id}.info`. Реальное смещение берётся из размера
  файла данных (так же, как это делает сам tusd), процент = `offset/size`.
- **Постобработка** запускается через канал завершённых загрузок tusd
  (`NotifyCompleteUploads`); результаты пишутся в сайдкары `{id}.meta.json`
  (ffprobe) и `{id}.jpg` (превью). Пользовательские поля (`title`, `tags`)
  хранятся в `{id}.user.json`.
- **Валидация** — pre-create hook отклоняет не-`video/*` (HTTP 415).
- **CFR-обработка.** Пока статус `checking` или `converting`, video-операции
  (`download`, frame, proxy, thumbnail, exports) недоступны. В `ready`
  `/download` и все анализаторы используют рабочий файл; `/original` всегда
  отдаёт исходник. После ошибки доступна ручная повторная обработка. Перезапуск
  сервиса переводит незавершённую обработку в `failed`.
- **Масштабирование** — локальный `filestore` можно заменить на `s3store` без
  изменения API.
