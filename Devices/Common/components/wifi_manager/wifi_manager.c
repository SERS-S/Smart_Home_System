#include <string.h>

#include "esp_log.h"
#include "esp_event.h"
#include "esp_netif.h"
#include "esp_wifi.h"
#include "esp_eap_client.h"

#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"

#include "wifi_manager.h"
#include "secrets.h"


static const char *TAG = "wifi_manager";


static EventGroupHandle_t wifi_event_group;

#define WIFI_CONNECTED_BIT BIT0


// ---------- Event handler ----------

static void wifi_event_handler(
    void *arg,
    esp_event_base_t event_base,
    int32_t event_id,
    void *event_data
)
{
    (void)arg;
    (void)event_data;

    if (event_base == WIFI_EVENT &&
        event_id == WIFI_EVENT_STA_START) {

        ESP_LOGI(TAG, "Connecting to Wi-Fi");

        esp_wifi_connect();

    } else if (event_base == WIFI_EVENT &&
               event_id == WIFI_EVENT_STA_DISCONNECTED) {

        ESP_LOGW(
            TAG,
            "Wi-Fi disconnected; reconnecting"
        );

        xEventGroupClearBits(
            wifi_event_group,
            WIFI_CONNECTED_BIT
        );

        esp_wifi_connect();

    } else if (event_base == IP_EVENT &&
               event_id == IP_EVENT_STA_GOT_IP) {

        ESP_LOGI(
            TAG,
            "Wi-Fi connected; IP address acquired"
        );

        xEventGroupSetBits(
            wifi_event_group,
            WIFI_CONNECTED_BIT
        );
    }
}


// ---------- Initialization ----------

esp_err_t wifi_manager_init(void)
{
    wifi_event_group = xEventGroupCreate();

    if (wifi_event_group == NULL) {
        ESP_LOGE(
            TAG,
            "Failed to create Wi-Fi event group"
        );

        return ESP_ERR_NO_MEM;
    }

    ESP_ERROR_CHECK(esp_netif_init());

    ESP_ERROR_CHECK(
        esp_event_loop_create_default()
    );

    esp_netif_create_default_wifi_sta();

    const wifi_init_config_t cfg =
        WIFI_INIT_CONFIG_DEFAULT();

    ESP_ERROR_CHECK(
        esp_wifi_init(&cfg)
    );

    ESP_ERROR_CHECK(
        esp_event_handler_register(
            WIFI_EVENT,
            ESP_EVENT_ANY_ID,
            wifi_event_handler,
            NULL
        )
    );

    ESP_ERROR_CHECK(
        esp_event_handler_register(
            IP_EVENT,
            IP_EVENT_STA_GOT_IP,
            wifi_event_handler,
            NULL
        )
    );

    wifi_config_t wifi_config = {0};

    strlcpy(
        (char *)wifi_config.sta.ssid,
        WIFI_SSID,
        sizeof(wifi_config.sta.ssid)
    );

    ESP_ERROR_CHECK(
        esp_wifi_set_mode(WIFI_MODE_STA)
    );

    ESP_ERROR_CHECK(
        esp_wifi_set_config(
            WIFI_IF_STA,
            &wifi_config
        )
    );

    // WPA2-Enterprise configuration.

    ESP_ERROR_CHECK(
        esp_eap_client_set_identity(
            (const unsigned char *)WIFI_USERNAME,
            strlen(WIFI_USERNAME)
        )
    );

    ESP_ERROR_CHECK(
        esp_eap_client_set_username(
            (const unsigned char *)WIFI_USERNAME,
            strlen(WIFI_USERNAME)
        )
    );

    ESP_ERROR_CHECK(
        esp_eap_client_set_password(
            (const unsigned char *)WIFI_PASSWORD,
            strlen(WIFI_PASSWORD)
        )
    );

    ESP_ERROR_CHECK(
        esp_wifi_sta_enterprise_enable()
    );

    ESP_ERROR_CHECK(
        esp_wifi_start()
    );

    return ESP_OK;
}


// ---------- Wait for connection ----------

void wifi_manager_wait_connected(void)
{
    if (wifi_event_group == NULL) {
        ESP_LOGE(
            TAG,
            "Wi-Fi manager is not initialized"
        );

        return;
    }

    xEventGroupWaitBits(
        wifi_event_group,
        WIFI_CONNECTED_BIT,
        pdFALSE,       // Не очищать бит после ожидания.
        pdTRUE,        // Ждать все указанные биты.
        portMAX_DELAY  // Ждать бесконечно.
    );
}